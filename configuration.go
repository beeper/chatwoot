package main

import (
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

type Configuration struct {
	// Authentication settings
	Homeserver   string `yaml:"homeserver"`
	Username     string `yaml:"username"`
	PasswordFile string `yaml:"password_file"`

	// Chatwoot Authentication
	ChatwootBaseUrl         string `yaml:"chatwoot_base_url"`
	ChatwootAccessTokenFile string `yaml:"chatwoot_access_token_file"`
	ChatwootAccountID       int    `yaml:"chatwoot_account_id"`
	ChatwootInboxID         int    `yaml:"chatwoot_inbox_id"`

	// Database settings
	DBConnectionString string `yaml:"db_connection_string"`

	// Bot settings
	AllowMessagesFromUsersOnOtherHomeservers bool   `yaml:"allow_messages_from_users_on_other_homeservers"`
	CanonicalDMPrefix                        string `yaml:"canonical_dm_prefix"`
	BridgeIfMembersLessThan                  int    `yaml:"bridge_if_members_less_than"`
	RenderMarkdown                           bool   `yaml:"render_markdown"`

	// Webhook listener settings
	ListenPort int `yaml:"listen_port"`
}

func (c *Configuration) GetPassword() (string, error) {
	log.Debug("Reading password from ", c.PasswordFile)
	buf, err := os.ReadFile(c.PasswordFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(buf)), nil
}

func (c *Configuration) GetChatwootAccessToken() (string, error) {
	log.Debug("Reading access token from ", c.ChatwootAccessTokenFile)
	buf, err := os.ReadFile(c.ChatwootAccessTokenFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(buf)), nil
}
