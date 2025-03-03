package app

import (
	"errors"
	"fmt"
	"log"
	"oneinstack/internal/models"
	"oneinstack/utils"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func init() {
	utils.EnsureOneDir() // 新增目录检查
	if err := InitDB(GetBasePath() + "myadmin.db"); err != nil {
		log.Fatal("InitDB error:", err)
	}
}

func InitDB(dbPath string) error {
	gc := &gorm.Config{}
	gc.Logger = logger.Default.LogMode(logger.Info)
	if ENV == "debug" {
		gc.Logger = logger.Default.LogMode(logger.Info)
	}

	d, err := gorm.Open(sqlite.Open(dbPath + "?_foreign_keys=1"))
	if err != nil {
		return err
	}
	db = d
	// 检查是否存在用户，如果不存在提示创建管理员
	err = createTables()
	if err != nil {
		log.Fatal("failed to migrate the database:", err)
	}

	return nil
}

func createTables() error {
	err := db.AutoMigrate(&models.System{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.User{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.Storage{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.Library{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.Software{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.Website{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.Remark{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.Dictionary{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.IptablesRule{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.CronJob{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.JobExecution{})
	if err != nil {
		return err
	}
	err = initSoftware()
	if err != nil {
		return err
	}
	err = initDic()
	if err != nil {
		return err
	}
	err = initRemark()
	if err != nil {
		return err
	}
	err = InitSystem()
	if err != nil {
		return err
	}
	return nil
}

func initSoftware() error {
	// 按依赖顺序迁移表
	tables := []interface{}{
		&models.Softwaren{},
		&models.Version{},
		&models.InstallConfig{},
		&models.ServiceConfig{},
		&models.ConfigParam{},
		&models.ConfigTemplate{},
		&models.PreCmd{},
		&models.DownloadURL{},
	}

	for _, table := range tables {
		if err := db.AutoMigrate(table); err != nil {
			return err
		}
	}

	// 初始化软件数据
	var count int64
	if err := db.Model(&models.Softwaren{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	// 创建Java
	java := &models.Softwaren{
		Name:        "java",
		Description: "Java开发工具包",
		Versions: []models.Version{
			{
				Version:     "7u80",
				VersionName: "jdk",
				DownloadURL: []models.DownloadURL{
					{
						URL: "https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/soft/jdk-7u80-linux-x64.tar.gz",
						OS:  "ubuntu",
					},
					{
						URL: "https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/soft/jdk-7u80-linux-x64.tar.gz",
						OS:  "centos",
					},
				},
				InstallConfig: models.InstallConfig{
					BasePath:     "{{.root}}/{{.name}}/v{{.version}}",
					ConfigParams: nil,
					ServiceConfig: models.ServiceConfig{
						SystemdTemplate: "",
					},
					ConfigTemplates: nil,
				},
			},
			{
				Version:     "17.0.8",
				VersionName: "jdk",
				DownloadURL: []models.DownloadURL{
					{
						URL: "https://download.java.net/openjdk/jdk17/ri/openjdk-17+35_linux-x64_bin.tar.gz",
						OS:  "ubuntu",
					},
					{
						URL: "https://download.java.net/openjdk/jdk17/ri/openjdk-17+35_linux-x64_bin.tar.gz",
						OS:  "centos",
					},
				},
				InstallConfig: models.InstallConfig{
					BasePath:     "{{.root}}/{{.name}}/v{{.version}}",
					ConfigParams: nil,
					ServiceConfig: models.ServiceConfig{
						SystemdTemplate: "",
					},
					ConfigTemplates: nil,
				},
			},
			{
				Version:     "11.0.20",
				VersionName: "jdk",
				DownloadURL: []models.DownloadURL{
					{
						URL: "https://download.java.net/openjdk/jdk11/ri/openjdk-11+28_linux-x64_bin.tar.gz",
						OS:  "ubuntu",
					},
					{
						URL: "https://download.java.net/openjdk/jdk11/ri/openjdk-11+28_linux-x64_bin.tar.gz",
						OS:  "centos",
					},
				},
				InstallConfig: models.InstallConfig{
					BasePath:     "{{.root}}/{{.name}}/v{{.version}}",
					ConfigParams: nil,
					ServiceConfig: models.ServiceConfig{
						SystemdTemplate: "",
					},
					ConfigTemplates: nil,
				},
			},
		},
	}

	// 创建Caddy
	caddy := &models.Softwaren{
		Name:        "caddy",
		Description: "现代Web服务器",
		Versions: []models.Version{
			{
				Version:     "2.9.1",
				VersionName: "caddy",
				DownloadURL: []models.DownloadURL{
					{
						URL: "https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/soft/caddy_2.9.1_linux_amd64.tar.gz",
						OS:  "ubuntu",
					},
					{
						URL: "https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/soft/caddy_2.9.1_linux_amd64.tar.gz",
						OS:  "centos",
					},
				},
				InstallConfig: models.InstallConfig{
					BasePath: "{{.root}}/{{.name}}/v{{.version}}",
					ConfigParams: []models.ConfigParam{
						{
							ConfigFile:  "Caddyfile",
							Name:        "admin_email",
							Type:        "string",
							Description: "管理员邮箱(用于HTTPS证书)",
						},
						{
							ConfigFile:  "Caddyfile",
							Name:        "domain",
							Type:        "string",
							Description: "主域名",
							Required:    true,
						},
					},
					ServiceConfig: models.ServiceConfig{
						StartCmd:        "{{.bin}}/caddy run --config {{.conf}}/Caddyfile",
						ReloadCmd:       "{{.bin}}/caddy reload --config {{.conf}}/Caddyfile",
						SystemdTemplate: "[Unit]\nDescription=Caddy Web Server\nAfter=network.target\n\n[Service]\nExecStart={{.start_cmd}}\nRestart=always\nEnvironment=CADDY_EMAIL={{.params.admin_email}}\n\n[Install]\nWantedBy=multi-user.target",
					},
					ConfigTemplates: []models.ConfigTemplate{
						{
							FileName: "Caddyfile",
							Content:  "{{.domain}} {\n respond \"Hello from Caddy\"\n}",
						},
					},
				},
			},
		},
	}

	// 创建PHP
	php := &models.Softwaren{
		Name:        "php",
		Description: "PHP脚本解释器",
		Versions: []models.Version{
			{
				Version:     "8.2.20",
				VersionName: "php-fpm",
				DownloadURL: []models.DownloadURL{
					{
						URL: "https://www.php.net/distributions/php-8.2.20.tar.gz",
						OS:  "ubuntu",
					},
					{
						URL: "https://www.php.net/distributions/php-8.2.20.tar.gz",
						OS:  "centos",
					},
				},
				InstallConfig: models.InstallConfig{
					BasePath: "{{.root}}/{{.name}}/v{{.version}}",
					ConfigParams: []models.ConfigParam{
						{
							ConfigFile:   "php.ini",
							Name:         "max_execution_time",
							Type:         "int",
							DefaultValue: "30",
							Description:  "最大执行时间（秒）",
						},
						{
							ConfigFile:   "php.ini",
							Name:         "memory_limit",
							Type:         "string",
							DefaultValue: "128M",
							Description:  "内存限制",
						},
					},
					ServiceConfig: models.ServiceConfig{
						StartCmd:        "{{.bin}}/php-fpm -y {{.conf}}/php-fpm.conf",
						SystemdTemplate: "[Unit]\nDescription=PHP FastCGI Process Manager\nAfter=network.target\n\n[Service]\nExecStart={{.start_cmd}}\nRestart=always\n\n[Install]\nWantedBy=multi-user.target",
					},
					ConfigTemplates: []models.ConfigTemplate{
						{
							FileName: "php.ini",
							Content:  "max_execution_time = {{.params.max_execution_time}}\nmemory_limit = {{.params.memory_limit}}",
						},
					},
				},
			},
		},
	}

	// 创建Redis
	redis := &models.Softwaren{
		Name:        "redis",
		Description: "内存数据库",
		Versions: []models.Version{
			{
				Version:     "7.2.4",
				VersionName: "redis-server",
				DownloadURL: []models.DownloadURL{
					{
						URL: "https://download.redis.io/releases/redis-7.2.4.tar.gz",
						OS:  "ubuntu",
					},
					{
						URL: "https://download.redis.io/releases/redis-7.2.4.tar.gz",
						OS:  "centos",
					},
				},
				InstallConfig: models.InstallConfig{
					BasePath: "{{.root}}/{{.name}}/v{{.version}}",
					ConfigParams: []models.ConfigParam{
						{
							ConfigFile:   "redis.conf",
							Name:         "port",
							Type:         "int",
							Description:  "服务端口",
							DefaultValue: "6379",
						},
						{
							ConfigFile:   "redis.conf",
							Name:         "maxmemory",
							Type:         "string",
							Description:  "最大内存",
							DefaultValue: "256mb",
						},
					},
					ServiceConfig: models.ServiceConfig{
						StartCmd:        "{{.bin}}/redis-server {{.conf}}/redis.conf",
						ReloadCmd:       "{{.bin}}/redis-cli config rewrite",
						SystemdTemplate: "[Unit]\nDescription=Redis In-Memory Data Store\nAfter=network.target\n\n[Service]\nExecStart={{.start_cmd}}\nRestart=always\n\n[Install]\nWantedBy=multi-user.target",
					},
					ConfigTemplates: []models.ConfigTemplate{
						{
							FileName: "redis.conf",
							Content:  "port {{.params.port}}\nmaxmemory {{.params.maxmemory}}",
						},
					},
				},
			},
			{
				Version:     "6.2.14",
				VersionName: "redis-server",
				DownloadURL: []models.DownloadURL{
					{
						URL: "https://download.redis.io/releases/redis-7.2.4.tar.gz",
						OS:  "ubuntu",
					},
					{
						URL: "https://download.redis.io/releases/redis-7.2.4.tar.gz",
						OS:  "centos",
					},
				},
				InstallConfig: models.InstallConfig{
					BasePath: "{{.root}}/{{.name}}/v{{.version}}",
					ConfigParams: []models.ConfigParam{
						{
							ConfigFile:   "redis.conf",
							Name:         "port",
							Type:         "int",
							Description:  "服务端口",
							DefaultValue: "6379",
						},
						{
							ConfigFile:   "redis.conf",
							Name:         "maxmemory",
							Type:         "string",
							Description:  "最大内存",
							DefaultValue: "256mb",
						},
					},
					ServiceConfig: models.ServiceConfig{
						StartCmd:        "{{.bin}}/redis-server {{.conf}}/redis.conf",
						ReloadCmd:       "{{.bin}}/redis-cli config rewrite",
						SystemdTemplate: "[Unit]\nDescription=Redis In-Memory Data Store\nAfter=network.target\n\n[Service]\nExecStart={{.start_cmd}}\nRestart=always\n\n[Install]\nWantedBy=multi-user.target",
					},
					ConfigTemplates: []models.ConfigTemplate{
						{
							FileName: "redis.conf",
							Content:  "port {{.params.port}}\nmaxmemory {{.params.maxmemory}}",
						},
					},
				},
			},
		},
	}

	// 创建MySQL
	mysql := &models.Softwaren{
		Name:        "mysql",
		Description: "关系型数据库",
		Versions: []models.Version{
			{
				Version:     "8.4.3",
				VersionName: "mysql-server",
				DownloadURL: []models.DownloadURL{
					{
						URL: "https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/soft/mysql-8.4.3-linux-glibc2.28-x86_64.tar.xz",
						OS:  "ubuntu",
					},
					{
						URL: "https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/soft/mysql-8.4.3-linux-glibc2.28-x86_64.tar.xz",
						OS:  "centos",
					},
				},
				InstallConfig: models.InstallConfig{
					BasePath: "{{.root}}/{{.name}}/v{{.version}}",
					ConfigParams: []models.ConfigParam{
						{
							ConfigFile:  "my.cnf",
							Name:        "root_password",
							Type:        "string",
							Description: "root用户密码",
							Sensitive:   true,
							Required:    true,
						},
						{
							ConfigFile:   "my.cnf",
							Name:         "port",
							Type:         "int",
							Description:  "服务端口",
							DefaultValue: "3306",
						},
					},
					ServiceConfig: models.ServiceConfig{
						StartCmd:        "{{.bin}}/mysqld --defaults-file={{.conf}}/my.cnf --initialize-insecure && {{.bin}}/mysqld_safe --defaults-file={{.conf}}/my.cnf",
						SystemdTemplate: "[Unit]\nDescription=MySQL Database Server\nAfter=network.target\n\n[Service]\nExecStart={{.bin}}/mysqld --defaults-file={{.conf}}/my.cnf\nRestart=always\nEnvironment=MYSQL_ROOT_PASSWORD={{.params.root_password}}\n\n[Install]\nWantedBy=multi-user.target",
					},
					ConfigTemplates: []models.ConfigTemplate{
						{
							FileName: "my.cnf",
							Content:  "[mysqld]\nport = {{.params.port}}\nbind-address = 0.0.0.0",
						},
					},
				},
			},
			{
				Version:     "5.7.44",
				VersionName: "mysql-server",
				DownloadURL: []models.DownloadURL{
					{
						URL: "https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/soft/mysql-5.7.44-linux-glibc2.12-x86_64.tar.gz",
						OS:  "ubuntu",
					},
					{
						URL: "https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/soft/mysql-5.7.44-linux-glibc2.12-x86_64.tar.gz",
						OS:  "centos",
					},
				},
				InstallConfig: models.InstallConfig{
					BasePath: "{{.root}}/{{.name}}/v{{.version}}",
					ConfigParams: []models.ConfigParam{
						{
							ConfigFile:  "my.cnf",
							Name:        "root_password",
							Type:        "string",
							Description: "root用户密码",
							Sensitive:   true,
							Required:    true,
						},
						{
							ConfigFile:   "my.cnf",
							Name:         "port",
							Type:         "int",
							Description:  "服务端口",
							DefaultValue: "3306",
						},
					},
					ServiceConfig: models.ServiceConfig{
						StartCmd:        "{{.bin}}/mysqld --defaults-file={{.conf}}/my.cnf --initialize-insecure && {{.bin}}/mysqld_safe --defaults-file={{.conf}}/my.cnf",
						SystemdTemplate: "[Unit]\nDescription=MySQL Database Server\nAfter=network.target\n\n[Service]\nExecStart={{.bin}}/mysqld --defaults-file={{.conf}}/my.cnf\nRestart=always\nEnvironment=MYSQL_ROOT_PASSWORD={{.params.root_password}}\n\n[Install]\nWantedBy=multi-user.target",
					},
					ConfigTemplates: []models.ConfigTemplate{
						{
							FileName: "my.cnf",
							Content:  "[mysqld]\nport = {{.params.port}}\nbind-address = 0.0.0.0",
						},
					},
				},
			},
		},
	}

	// 创建Nginx
	nginx := &models.Softwaren{
		Name:        "nginx",
		Description: "高性能Web服务器",
		Versions: []models.Version{
			{
				Version:     "1.24.0",
				VersionName: "nginx",
				DownloadURL: []models.DownloadURL{
					{
						URL:     "https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/soft/nginx-1.24.0-ubuntu.tar.gz",
						OS:      "ubuntu",
						Version: 2004,
					},
					{
						URL:     "https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/soft/nginx-1.24.0-cent.tar.gz",
						OS:      "centos",
						Version: 8,
					},
				},
				PreCmd: []models.PreCmd{
					{
						Cmd: " apt-get update && apt-get install -y build-essential libpcre3 libpcre3-dev libssl-dev zlib1g-dev wget",
						OS:  "ubuntu",
					},
					{
						Cmd: ` yum -y install gcc gcc-c++ autoconf automake make`,
						OS:  "centos",
					},
					{
						Cmd: ` yum -y install gcc gcc-c++ autoconf automake make  `,
						OS:  "centos",
					},
					{
						Cmd: ` yum -y install pcre-devel openssl-devel  `,
						OS:  "centos",
					},
				},
				InstallConfig: models.InstallConfig{
					BasePath:     "{{.root}}/{{.name}}/v{{.version}}",
					ConfigParams: nil,
					ServiceConfig: models.ServiceConfig{
						StartCmd:  "{{.bin}}/nginx -c {{.bin}}/nginx.conf",
						ReloadCmd: "{{.bin}}/nginx -s reload",
						SystemdTemplate: `
[Unit]
Description=NGINX Web Server
After=network.target

[Service]
Type=forking
ExecStartPre=/usr/local/onesoft/nginx/v1.24.0/bin/nginx -t
ExecStart=/usr/local/onesoft/nginx/v1.24.0/bin/nginx
PIDFile=/usr/local/onesoft/nginx/v1.24.0/bin/nginx.pid
Restart=on-failure

[Install]
WantedBy=multi-user.target
						`,
					},
					ConfigTemplates: nil,
					Cmd:             "",
				},
			},
		},
	}

	// 在事务中创建所有软件
	tx := db.Begin()
	softwareList := []*models.Softwaren{caddy, php, java, redis, mysql, nginx}
	for _, soft := range softwareList {
		if err := tx.Create(soft).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit().Error
}

func initDic() error {
	r := []*models.Dictionary{{
		Key:   "数据库",
		Value: "数据库",
		Q:     "soft_tags",
	},
		{
			Key:   "缓存",
			Value: "缓存",
			Q:     "soft_tags",
		},
		{
			Key:   "web服务",
			Value: "web服务",
			Q:     "soft_tags",
		},
		{
			Key:   "php",
			Value: "php",
			Q:     "soft_tags",
		}}
	var dic models.Dictionary
	result := db.First(&dic)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return result.Error
	}
	if dic.ID > 0 {
		return nil
	}
	tx := db.CreateInBatches(r, len(r))
	return tx.Error
}

func initRemark() error {
	r := &models.Remark{
		Content: "",
	}
	result := db.First(r)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return result.Error
	}
	if r.ID > 0 {
		return nil
	}
	tx := db.Create(r)
	return tx.Error
}

func InitUser() error {
	var count int64 = 0
	tx := DB().Model(models.User{}).Count(&count)
	if tx.Error != nil {
		return tx.Error
	}
	if count > 0 {
		return nil
	}
	err := setupAdminUser()
	if err != nil {
		return err
	}
	return nil
}

func setupAdminUser() error {
	username := utils.GenerateRandomString(8, 12)
	password := utils.GenerateRandomString(8, 12) // 生成 8-12 位随机密码
	hashed, err := utils.HashPassword(password)
	if err != nil {
		return err
	}
	user := &models.User{
		Username: username,
		Password: hashed,
		IsAdmin:  true,
	}
	tx := DB().Create(user)
	if tx.Error != nil {
		return tx.Error
	}
	fmt.Printf("用户创建成功.\n用户名: %s\n用户密码: %s\n", username, password)
	return nil
}

func getAdminUser() error {
	var user models.User
	tx := DB().First(&user)
	if tx.Error != nil {
		return tx.Error
	}
	fmt.Printf("用户创建成功.\n用户名: %s\n用户密码: %s\n", user.Username, user.Password)
	return nil
}

func InitSystem() error {
	r := &models.System{
		Title: "OneStack",
	}
	var count int64 = 0
	tx := DB().Model(models.System{}).Count(&count)
	if tx.Error != nil {
		return tx.Error
	}
	if count > 0 {
		return nil
	}
	tx = DB().Create(r)
	return tx.Error
}
