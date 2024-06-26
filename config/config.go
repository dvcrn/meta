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

package config

import (
	"maunium.net/go/mautrix/bridge/bridgeconfig"
	"maunium.net/go/mautrix/id"
)

type BridgeMode string

const (
	ModeInstagram   BridgeMode = "instagram"
	ModeFacebook    BridgeMode = "facebook"
	ModeFacebookTor BridgeMode = "facebook-tor"
	ModeMessenger   BridgeMode = "messenger"
)

func (bm BridgeMode) IsValid() bool {
	return bm == ModeInstagram || bm == ModeFacebook || bm == ModeFacebookTor || bm == ModeMessenger
}

func (bm BridgeMode) IsMessenger() bool {
	return bm == ModeFacebook || bm == ModeFacebookTor || bm == ModeMessenger
}

func (bm BridgeMode) IsInstagram() bool {
	return bm == ModeInstagram
}

type Config struct {
	*bridgeconfig.BaseConfig `yaml:",inline"`

	Meta struct {
		Mode                            BridgeMode `yaml:"mode"`
		IGE2EE                          bool       `yaml:"ig_e2ee"`
		Proxy                           string     `yaml:"proxy"`
		GetProxyFrom                    string     `yaml:"get_proxy_from"`
		MinFullReconnectIntervalSeconds int        `yaml:"min_full_reconnect_interval_seconds"`
		ForceRefreshIntervalSeconds     int        `yaml:"force_refresh_interval_seconds"`
	} `yaml:"meta"`

	Bridge BridgeConfig `yaml:"bridge"`
}

func (config *Config) CanAutoDoublePuppet(userID id.UserID) bool {
	_, homeserver, _ := userID.Parse()
	_, hasSecret := config.Bridge.DoublePuppetConfig.SharedSecretMap[homeserver]

	return hasSecret
}
