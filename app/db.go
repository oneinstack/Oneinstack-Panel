package app

import (
	"errors"
	"fmt"
	"log"
	"oneinstack/internal/models"
	"oneinstack/utils"

	_ "github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
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
	gc.Logger = logger.Default.LogMode(logger.Error)
	if ENV == "debug" {
		gc.Logger = logger.Default.LogMode(logger.Info)
	}

	d, err := gorm.Open(sqlite.Open(dbPath))
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
	err := db.AutoMigrate(&models.Softwaren{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.Version{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.InstallConfig{})
	if err != nil {
		return err
	}
	err = db.AutoMigrate(&models.ConfigParam{})
	if err != nil {
		return err
	}

	// 初始化软件数据
	var count int64
	if err := db.Model(&models.Softwaren{}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	// 创建Caddy软件
	caddy := &models.Softwaren{
		Name:        "caddy",
		Description: "现代Web服务器",
		Versions: []models.Version{
			{
				Version:     "2.7.5",
				VersionName: "caddy",
				DownloadURL: "https://github.com/caddyserver/caddy/releases/download/v2.7.5/caddy_2.7.5_linux_amd64.tar.gz",
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
							Content:  "{{.params.domain}} {\n respond \"Hello from Caddy\"\n}",
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
				Version:     "8.3.7",
				VersionName: "php-fpm",
				DownloadURL: "https://www.php.net/distributions/php-8.3.7.tar.gz",
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

	// 批量创建软件数据
	if err := db.CreateInBatches([]*models.Softwaren{caddy, php}, 2).Error; err != nil {
		return err
	}

	return nil
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
