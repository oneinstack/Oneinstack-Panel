package models

type Dictionary struct {
	ID    int64  `json:"id"`
	Key   string `json:"key"`
	Value string `json:"value"`
	Q     string `json:"q"`
}
