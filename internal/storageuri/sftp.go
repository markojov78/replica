// Package storageuri validates backend locations shared by services and drivers.
package storageuri

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// ParseSFTP returns a canonical location in the server's filesystem namespace.
func ParseSFTP(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "sftp" || u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !path.IsAbs(u.Path) || strings.ContainsAny(u.Path, "\x00\\") {
		return nil, fmt.Errorf("invalid SFTP URI")
	}
	if u.User != nil {
		if _, password := u.User.Password(); password || u.User.Username() == "" {
			return nil, fmt.Errorf("SFTP URI must not contain a password")
		}
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == ".." {
			return nil, fmt.Errorf("SFTP URI must not contain parent traversal")
		}
	}
	port := u.Port()
	if port == "" {
		port = "22"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, fmt.Errorf("invalid SFTP port")
	}
	u.Host = net.JoinHostPort(strings.ToLower(u.Hostname()), strconv.Itoa(n))
	u.Path = path.Clean(u.Path)
	u.RawPath = ""
	return u, nil
}
