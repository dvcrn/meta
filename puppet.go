// mautrix-meta - A Matrix-Facebook Messenger and Instagram DM puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"sync"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/bridge"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-meta/config"
	"go.mau.fi/mautrix-meta/database"
	"go.mau.fi/mautrix-meta/messagix"
	"go.mau.fi/mautrix-meta/messagix/types"
)

func (br *MetaBridge) GetPuppetByMXID(mxid id.UserID) *Puppet {
	userID, ok := br.ParsePuppetMXID(mxid)
	if !ok {
		return nil
	}

	return br.GetPuppetByID(userID)
}

func (br *MetaBridge) GetPuppetByID(id int64) *Puppet {
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()
	if id == 0 {
		panic("User ID not provided")
	}

	puppet, ok := br.puppets[id]
	if !ok {
		dbPuppet, err := br.DB.Puppet.GetByID(context.TODO(), id)
		if err != nil {
			br.ZLog.Err(err).Msg("Failed to get puppet from database")
			return nil
		}
		return br.loadPuppet(context.TODO(), dbPuppet, &id)
	}
	return puppet
}

func (br *MetaBridge) GetPuppetByCustomMXID(mxid id.UserID) *Puppet {
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()

	puppet, ok := br.puppetsByCustomMXID[mxid]
	if !ok {
		dbPuppet, err := br.DB.Puppet.GetByCustomMXID(context.TODO(), mxid)
		if err != nil {
			br.ZLog.Err(err).Msg("Failed to get puppet from database")
			return nil
		}
		return br.loadPuppet(context.TODO(), dbPuppet, nil)
	}
	return puppet
}

func (br *MetaBridge) GetAllPuppetsWithCustomMXID() []*Puppet {
	puppets, err := br.DB.Puppet.GetAllWithCustomMXID(context.TODO())
	if err != nil {
		br.ZLog.Error().Err(err).Msg("Failed to get all puppets with custom MXID")
		return nil
	}
	return br.dbPuppetsToPuppets(puppets)
}

func (br *MetaBridge) FormatPuppetMXID(userID int64) id.UserID {
	return id.NewUserID(
		br.Config.Bridge.FormatUsername(strconv.FormatInt(userID, 10)),
		br.Config.Homeserver.Domain,
	)
}

func (br *MetaBridge) loadPuppet(ctx context.Context, dbPuppet *database.Puppet, userID *int64) *Puppet {
	if dbPuppet == nil {
		if userID == nil {
			return nil
		}
		dbPuppet = br.DB.Puppet.New()
		dbPuppet.ID = *userID
		err := dbPuppet.Insert(ctx)
		if err != nil {
			br.ZLog.Error().Err(err).Int64("user_id", *userID).Msg("Failed to insert new puppet")
			return nil
		}
	}

	puppet := br.NewPuppet(dbPuppet)
	br.puppets[puppet.ID] = puppet
	if puppet.CustomMXID != "" {
		br.puppetsByCustomMXID[puppet.CustomMXID] = puppet
	}
	return puppet
}

func (br *MetaBridge) dbPuppetsToPuppets(dbPuppets []*database.Puppet) []*Puppet {
	br.puppetsLock.Lock()
	defer br.puppetsLock.Unlock()

	output := make([]*Puppet, len(dbPuppets))
	for index, dbPuppet := range dbPuppets {
		if dbPuppet == nil {
			continue
		}
		puppet, ok := br.puppets[dbPuppet.ID]
		if !ok {
			puppet = br.loadPuppet(context.TODO(), dbPuppet, nil)
		}
		output[index] = puppet
	}
	return output
}

func (br *MetaBridge) NewPuppet(dbPuppet *database.Puppet) *Puppet {
	return &Puppet{
		Puppet: dbPuppet,
		bridge: br,
		log:    br.ZLog.With().Int64("user_id", dbPuppet.ID).Logger(),

		MXID: br.FormatPuppetMXID(dbPuppet.ID),
	}
}

func (br *MetaBridge) ParsePuppetMXID(mxid id.UserID) (int64, bool) {
	if userIDRegex == nil {
		pattern := fmt.Sprintf(
			"^@%s:%s$",
			br.Config.Bridge.FormatUsername(`(\d+)`),
			br.Config.Homeserver.Domain,
		)
		userIDRegex = regexp.MustCompile(pattern)
	}

	match := userIDRegex.FindStringSubmatch(string(mxid))
	if len(match) == 2 {
		parsed, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	}

	return 0, false
}

type Puppet struct {
	*database.Puppet

	bridge *MetaBridge
	log    zerolog.Logger

	MXID id.UserID

	customIntent *appservice.IntentAPI
	customUser   *User

	syncLock sync.Mutex
}

var userIDRegex *regexp.Regexp

var (
	_ bridge.Ghost            = (*Puppet)(nil)
	_ bridge.GhostWithProfile = (*Puppet)(nil)
)

func (puppet *Puppet) GetMXID() id.UserID {
	return puppet.MXID
}

func (puppet *Puppet) DefaultIntent() *appservice.IntentAPI {
	return puppet.bridge.AS.Intent(puppet.MXID)
}

func (puppet *Puppet) CustomIntent() *appservice.IntentAPI {
	if puppet == nil {
		return nil
	}
	return puppet.customIntent
}

func (puppet *Puppet) IntentFor(portal *Portal) *appservice.IntentAPI {
	if puppet != nil {
		if puppet.customIntent == nil || (portal.IsPrivateChat() && portal.ThreadID == puppet.ID) {
			return puppet.DefaultIntent()
		}
		return puppet.customIntent
	}
	return nil
}

func (puppet *Puppet) GetDisplayname() string {
	return puppet.Name
}

func (puppet *Puppet) GetAvatarURL() id.ContentURI {
	return puppet.AvatarURL
}

func (puppet *Puppet) UpdateInfo(ctx context.Context, info types.UserInfo) {
	log := zerolog.Ctx(ctx).With().
		Str("function", "Puppet.UpdateInfo").
		Int64("user_id", puppet.ID).
		Logger()
	ctx = log.WithContext(ctx)
	var err error
	if info == nil {
		log.Debug().Msg("Not Fetching info to update puppet")
		// TODO implement?
		return
	}

	log.Trace().Msg("Updating puppet info")

	update := false
	if puppet.Username != info.GetUsername() {
		puppet.Username = info.GetUsername()
		update = true
	}
	update = puppet.updateName(ctx, info.GetName(), puppet.Username) || update
	update = puppet.updateAvatar(ctx, info.GetAvatarURL()) || update
	if update {
		puppet.ContactInfoSet = false
		puppet.UpdateContactInfo(ctx)
		err = puppet.Update(ctx)
		if err != nil {
			log.Err(err).Msg("Failed to save puppet to database after updating")
		}
		go puppet.updatePortalMeta(ctx)
		log.Debug().Msg("Puppet info updated")
	}
}

func (puppet *Puppet) UpdateContactInfo(ctx context.Context) {
	if !puppet.bridge.SpecVersions.Supports(mautrix.BeeperFeatureArbitraryProfileMeta) || puppet.ContactInfoSet {
		return
	}

	identifiers := make([]string, 0, 1)
	if puppet.Username != "" {
		identifiers = append(identifiers, fmt.Sprintf("%s:%s", puppet.bridge.BeeperNetworkName, puppet.Username))
	}
	contactInfo := map[string]any{
		"com.beeper.bridge.identifiers": identifiers,
		"com.beeper.bridge.remote_id":   puppet.ID,
		"com.beeper.bridge.service":     puppet.bridge.BeeperServiceName,
		"com.beeper.bridge.network":     puppet.bridge.BeeperNetworkName,
	}
	err := puppet.DefaultIntent().BeeperUpdateProfile(ctx, contactInfo)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to store custom contact info in profile")
	} else {
		puppet.ContactInfoSet = true
	}
}

func (puppet *Puppet) updatePortalMeta(ctx context.Context) {
	for _, portal := range puppet.bridge.FindPrivateChatPortalsWith(puppet.ID) {
		// Get room create lock to prevent races between receiving contact info and room creation.
		portal.roomCreateLock.Lock()
		portal.UpdateInfoFromPuppet(ctx, puppet)
		portal.roomCreateLock.Unlock()
	}
}

var avatarHTTPClient http.Client

func downloadAvatar(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare request: %w", err)
	}
	req.Header.Set("Sec-Fetch-Dest", "image")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	// TODO use facebook.com when appropriate
	req.Header.Set("Referer", "https://www.instagram.com/")
	req.Header.Set("Accept", "image/avif,image/webp,*/*")
	req.Header.Set("User-Agent", messagix.UserAgent)
	resp, err := avatarHTTPClient.Do(req)
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	} else if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	} else if respData, err := io.ReadAll(resp.Body); err != nil {
		return nil, fmt.Errorf("failed to read response data: %w", err)
	} else {
		return respData, nil
	}
}

func updateAvatar(
	ctx context.Context,
	newAvatarURL string,
	avatarID *string, avatarSet *bool, avatarURL *id.ContentURI,
	intent *appservice.IntentAPI, setAvatar func(context.Context, id.ContentURI) error,
) bool {
	log := zerolog.Ctx(ctx)
	var newAvatarID string
	if newAvatarURL != "" {
		parsedAvatarURL, _ := url.Parse(newAvatarURL)
		newAvatarID = path.Base(parsedAvatarURL.Path)
	}
	if *avatarID == newAvatarID && (*avatarSet || setAvatar == nil) {
		return false
	}
	*avatarID = newAvatarID
	*avatarSet = false
	*avatarURL = id.ContentURI{}
	if newAvatarID == "" {
		if setAvatar == nil {
			return true
		}
		err := setAvatar(ctx, *avatarURL)
		if err != nil {
			log.Err(err).Msg("Failed to remove avatar")
			return true
		}
		log.Debug().Msg("Avatar removed")
		*avatarSet = true
		return true
	}
	avatarData, err := downloadAvatar(ctx, newAvatarURL)
	if err != nil {
		log.Err(err).
			Str("avatar_id", newAvatarID).
			Msg("Failed to download new avatar")
		return true
	}
	avatarContentType := http.DetectContentType(avatarData)
	resp, err := intent.UploadBytes(ctx, avatarData, avatarContentType)
	if err != nil {
		log.Err(err).
			Str("avatar_id", newAvatarID).
			Msg("Failed to upload new avatar")
		return true
	}
	*avatarURL = resp.ContentURI
	if setAvatar == nil {
		return true
	}
	err = setAvatar(ctx, *avatarURL)
	if err != nil {
		log.Err(err).Msg("Failed to update avatar")
		return true
	}
	log.Debug().
		Str("avatar_id", newAvatarID).
		Stringer("avatar_mxc", resp.ContentURI).
		Msg("Avatar updated successfully")
	*avatarSet = true
	return true
}

func (puppet *Puppet) updateAvatar(ctx context.Context, avatarURL string) bool {
	return updateAvatar(
		ctx, avatarURL,
		&puppet.AvatarID, &puppet.AvatarSet, &puppet.AvatarURL,
		puppet.DefaultIntent(), puppet.DefaultIntent().SetAvatarURL,
	)
}

func (puppet *Puppet) updateName(ctx context.Context, name, username string) bool {
	newName := puppet.bridge.Config.Bridge.FormatDisplayname(config.DisplaynameParams{
		DisplayName: name,
		Username:    username,
		ID:          puppet.ID,
	})
	if puppet.NameSet && puppet.Name == newName {
		return false
	}
	puppet.Name = newName
	puppet.NameSet = false
	err := puppet.DefaultIntent().SetDisplayName(ctx, newName)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to update user displayname")
	} else {
		puppet.NameSet = true
	}
	return true
}
