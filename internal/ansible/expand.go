package ansible

import (
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
)

// ExpandVars expands $var and ${var} from the environment, leaving unknown
// variables unchanged, like Python's os.path.expandvars. It scans for the
// same matches as Python's pattern \$(\w+|\{[^}]*\}?) with ASCII \w.
func ExpandVars(s string) string {
	if !strings.Contains(s, "$") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		var m, name string
		switch j := i + 1; {
		case isWordByte(s[j]):
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			m, name = s[i:j], s[i+1:j]
		case s[j] == '{':
			end := strings.IndexByte(s[j:], '}')
			if end < 0 {
				m = s[i:] // unterminated: Python matches it all and leaves it as is
			} else {
				m, name = s[i:j+end+1], s[j+1:j+end]
			}
		default:
			b.WriteByte('$')
			i++
			continue
		}
		if v, ok := os.LookupEnv(name); ok && name != "" {
			b.WriteString(v)
		} else {
			b.WriteString(m)
		}
		i += len(m)
	}
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ExpandUser expands a leading ~ or ~user, like Python's os.path.expanduser.
// Unknown users and a missing home leave the path unchanged.
func ExpandUser(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	i := strings.Index(path[1:], "/") + 1
	if i <= 0 {
		i = len(path)
	}
	var home string
	if i == 1 {
		if h, ok := os.LookupEnv("HOME"); ok {
			home = h
		} else if u, err := user.Current(); err == nil {
			home = u.HomeDir
		} else {
			return path
		}
	} else {
		u, ok := LookupUser(path[1:i])
		if !ok {
			return path
		}
		home = u.HomeDir
	}
	home = strings.TrimRight(home, "/")
	if out := home + path[i:]; out != "" {
		return out
	}
	return "/"
}

// ExpandPath is what AnsibleModule does for type='path' options:
// expanduser(expandvars(path)).
func ExpandPath(path string) string {
	return ExpandUser(ExpandVars(path))
}

// LookupUser finds a user by name in the user database. With CGO disabled
// os/user only reads /etc/passwd, so fall back to getent for NSS sources
// such as LDAP, as Python's pwd module would see them.
func LookupUser(name string) (*user.User, bool) {
	if u, err := user.Lookup(name); err == nil {
		return u, true
	}
	f, ok := Getent("passwd", name)
	if !ok || len(f) < 6 {
		return nil, false
	}
	return &user.User{Username: f[0], Uid: f[2], Gid: f[3], Name: f[4], HomeDir: f[5]}, true
}

// LookupGroupID finds a group's gid by name, falling back to getent like LookupUser.
func LookupGroupID(name string) (int, bool) {
	if g, err := user.LookupGroup(name); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			return gid, true
		}
	}
	f, ok := Getent("group", name)
	if !ok || len(f) < 3 {
		return 0, false
	}
	gid, err := strconv.Atoi(f[2])
	return gid, err == nil
}

// Getent looks key up in an NSS database (passwd, group) with getent.
func Getent(db, key string) ([]string, bool) {
	bin, err := exec.LookPath("getent")
	if err != nil {
		return nil, false
	}
	out, err := exec.Command(bin, db, key).Output()
	if err != nil {
		return nil, false
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return strings.Split(line, ":"), true
}

// UserName is pwd.getpwuid(uid).pw_name, with ok false when there is no such user.
func UserName(uid int) (string, bool) {
	id := strconv.Itoa(uid)
	if u, err := user.LookupId(id); err == nil {
		return u.Username, true
	}
	if f, ok := Getent("passwd", id); ok && len(f) > 0 && f[0] != "" {
		return f[0], true
	}
	return "", false
}

// GroupName is grp.getgrgid(gid).gr_name, with ok false when there is no such group.
func GroupName(gid int) (string, bool) {
	id := strconv.Itoa(gid)
	if g, err := user.LookupGroupId(id); err == nil {
		return g.Name, true
	}
	if f, ok := Getent("group", id); ok && len(f) > 0 && f[0] != "" {
		return strings.TrimSpace(f[0]), true
	}
	return "", false
}
