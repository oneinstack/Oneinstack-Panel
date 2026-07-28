package output

import "time"

type StorageConnection struct {
	ID                 int64     `json:"id"`
	Addr               string    `json:"addr"`
	Port               string    `json:"port"`
	Root               string    `json:"root"`
	Remark             string    `json:"remark"`
	Type               string    `json:"type"`
	PasswordConfigured bool      `json:"passwordConfigured"`
	Managed            bool      `json:"managed"`
	CreateTime         time.Time `json:"create_time"`
	UpdateTime         time.Time `json:"update_time"`
}

type DatabaseLibrary struct {
	ID                 int64     `json:"id"`
	PID                int64     `json:"pid"`
	Name               string    `json:"name"`
	User               string    `json:"user"`
	PasswordConfigured bool      `json:"passwordConfigured"`
	Encoding           string    `json:"encoding"`
	Capacity           string    `json:"capacity"`
	PAddr              string    `json:"p_addr"`
	Type               string    `json:"type"`
	CreateTime         time.Time `json:"create_time"`
}

// DatabaseCredential is returned only by explicit create, reveal, or password
// replacement operations. Normal list APIs continue to expose only whether a
// credential is configured.
type DatabaseCredential struct {
	LibraryID int64  `json:"libraryId"`
	Database  string `json:"database"`
	Username  string `json:"username"`
	Password  string `json:"password"`
}
