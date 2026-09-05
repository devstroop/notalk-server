package service

import (
	"fmt"
	"net/url"

	"github.com/devstroop/notalk/internal/database"
	"github.com/devstroop/notalk/internal/model"
)

// ProxyConfig holds structured proxy parameters for an account.
type ProxyConfig struct {
	Protocol string // http, https, socks5
	Host     string
	Port     int
	Username string
	Password string
	Enabled  bool
}

// URL builds a *url.URL from the structured proxy fields.
func (p *ProxyConfig) URL() *url.URL {
	u := &url.URL{
		Scheme: p.Protocol,
		Host:   fmt.Sprintf("%s:%d", p.Host, p.Port),
	}
	if p.Username != "" {
		if p.Password != "" {
			u.User = url.UserPassword(p.Username, p.Password)
		} else {
			u.User = url.User(p.Username)
		}
	}
	return u
}

// ToModel converts a ProxyConfig to the API-facing model.
func (p *ProxyConfig) ToModel() model.ProxyConfigResponse {
	return model.ProxyConfigResponse{
		Protocol: p.Protocol,
		Host:     p.Host,
		Port:     p.Port,
		Username: p.Username,
		Enabled:  p.Enabled,
	}
}

// ProxyConfigFromDB converts a database record to service ProxyConfig.
func ProxyConfigFromDB(rec *database.ProxyConfigRecord) *ProxyConfig {
	if rec == nil {
		return nil
	}
	return &ProxyConfig{
		Protocol: rec.Protocol,
		Host:     rec.Host,
		Port:     rec.Port,
		Username: rec.Username,
		Password: rec.Password,
		Enabled:  rec.Enabled,
	}
}

// ProxyConfigToDB converts a service ProxyConfig to a database record.
func ProxyConfigToDB(accountID string, p *ProxyConfig) *database.ProxyConfigRecord {
	return &database.ProxyConfigRecord{
		AccountID: accountID,
		Protocol:  p.Protocol,
		Host:      p.Host,
		Port:      p.Port,
		Username:  p.Username,
		Password:  p.Password,
		Enabled:   p.Enabled,
	}
}
