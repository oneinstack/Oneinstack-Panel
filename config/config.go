package config

type Server struct {
	System       System       `mapstructure:"system" json:"system" yaml:"system"`
	ScriptCenter ScriptCenter `mapstructure:"scriptCenter" json:"scriptCenter" yaml:"scriptCenter"`
	UpdateCenter UpdateCenter `mapstructure:"updateCenter" json:"updateCenter" yaml:"updateCenter"`
	Bastion      BastionHost  `mapstructure:"bastion" json:"bastion" yaml:"bastion"`
	ClusterAgent ClusterAgent `mapstructure:"clusterAgent" json:"clusterAgent" yaml:"clusterAgent"`
	Translation  Translation  `mapstructure:"translation" json:"translation" yaml:"translation"`
}
