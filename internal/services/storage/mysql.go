package storage

import (
	"context"
	"fmt"
	"log"
	"math"
	"net"
	"oneinstack/app"
	"oneinstack/internal/models"
	"regexp"
	"strings"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type MysqlOP struct {
	ID       int64
	Addr     string
	Port     string
	Root     string
	Password string
	Type     string
	Lib      string
	DB       *gorm.DB
}
type DbInfo struct {
	DbName   string
	Usage    float64
	Encoding string
}
type UserPrivilege struct {
	Db   string
	User string
	Host string
}

// Managed database users are reachable from local sites over TCP and from a
// separately configured Panel host. The MySQL service itself still binds to
// 127.0.0.1 by default, so this account host does not expose the server unless
// the administrator explicitly changes the network binding.
const managedMySQLAccountHost = "%"

func NewMysqlOP(p *models.Storage, lib string) *MysqlOP {
	return &MysqlOP{
		ID:       p.ID,
		Addr:     p.Addr,
		Port:     p.Port,
		Root:     p.Root,
		Password: p.Password,
		Type:     p.Type,
		DB:       nil,
		Lib:      lib,
	}
}

func (s *MysqlOP) Connect() error {
	return s.ConnectContext(context.Background())
}

func (s *MysqlOP) ConnectContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	driverConfig := mysqlDriver.Config{
		User:                 s.Root,
		Passwd:               s.Password,
		Net:                  "tcp",
		Addr:                 net.JoinHostPort(s.Addr, s.Port),
		DBName:               s.Lib,
		Collation:            "utf8mb4_unicode_ci",
		ParseTime:            true,
		Loc:                  time.Local,
		Timeout:              5 * time.Second,
		ReadTimeout:          15 * time.Second,
		WriteTimeout:         15 * time.Second,
		InterpolateParams:    true,
		AllowNativePasswords: true,
	}
	dsn := driverConfig.FormatDSN()
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{
		Logger:               logger.Default.LogMode(logger.Silent),
		DisableAutomaticPing: true,
	})
	if err != nil {
		return wrapConnectionError(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return wrapConnectionError(err)
	}
	pingContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingContext); err != nil {
		_ = sqlDB.Close()
		return wrapConnectionError(err)
	}
	s.DB = db
	return nil
}

func (s *MysqlOP) Close() error {
	if s.DB == nil {
		return nil
	}
	sqlDB, err := s.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (s *MysqlOP) Sync() error {
	// 获取所有数据库及其大小
	var dbInfos []DbInfo
	err := s.DB.Raw(`
		SELECT
			s.SCHEMA_NAME as DbName,
			COALESCE(SUM(t.DATA_LENGTH + t.INDEX_LENGTH), 0) as ` + "`Usage`" + `,
			s.DEFAULT_CHARACTER_SET_NAME as Encoding
		FROM information_schema.SCHEMATA s
		LEFT JOIN information_schema.TABLES t
			ON t.TABLE_SCHEMA = s.SCHEMA_NAME
		GROUP BY s.SCHEMA_NAME, s.DEFAULT_CHARACTER_SET_NAME
	`).Scan(&dbInfos).Error
	if err != nil {
		return err
	}
	ls := []models.Library{}
	// 获取每个数据库的用户权限信息
	for _, dbInfo := range dbInfos {
		if dbInfo.DbName == "information_schema" || dbInfo.DbName == "mysql" || dbInfo.DbName == "performance_schema" || dbInfo.DbName == "sys" {
			continue
		}
		var userPrivileges []UserPrivilege
		err = s.DB.Raw(`
			SELECT DISTINCT 
				DB, 
				User, 
				Host
			FROM 
				mysql.db
			WHERE 
				DB = ?
		`, dbInfo.DbName).Scan(&userPrivileges).Error
		if err != nil {
			return err
		}

		l := models.Library{
			PID:      s.ID,
			Name:     dbInfo.DbName,
			User:     "",
			Password: "",
			Encoding: dbInfo.Encoding,
			Capacity: ConvertBytes(dbInfo.Usage),
			PAddr:    fmt.Sprintf("%s:%v", s.Addr, s.Port),
			Type:     s.Type,
		}
		// 输出数据库信息和其访问用户
		fmt.Printf("Database: %s, Usage: %.2f bytes\n", dbInfo.DbName, dbInfo.Usage)
		if len(userPrivileges) > 0 {
			l.User = userPrivileges[0].User
		}
		ls = append(ls, l)
	}
	return app.DB().Transaction(func(tx *gorm.DB) error {
		return persistSyncedMySQLLibraries(tx, s.ID, ls)
	})
}

func persistSyncedMySQLLibraries(
	tx *gorm.DB,
	connectionID int64,
	discovered []models.Library,
) error {
	var existing []models.Library
	if err := tx.Where("p_id = ? AND type = ?", connectionID, "mysql").
		Find(&existing).Error; err != nil {
		return err
	}
	byName := make(map[string]*models.Library, len(existing))
	for i := range existing {
		byName[existing[i].Name] = &existing[i]
	}
	seen := make(map[int64]struct{}, len(discovered))
	for i := range discovered {
		item := &discovered[i]
		if current := byName[item.Name]; current != nil {
			updates := map[string]any{
				"capacity": item.Capacity,
				"p_addr":   item.PAddr,
				"type":     "mysql",
			}
			if strings.TrimSpace(item.User) != "" {
				updates["user"] = item.User
			}
			if strings.TrimSpace(item.Encoding) != "" {
				updates["encoding"] = item.Encoding
			}
			if err := tx.Model(&models.Library{}).Where("id = ?", current.ID).
				Updates(updates).Error; err != nil {
				return err
			}
			seen[current.ID] = struct{}{}
			continue
		}
		item.PID = connectionID
		item.Type = "mysql"
		if item.CreateTime.IsZero() {
			item.CreateTime = time.Now()
		}
		if err := tx.Create(item).Error; err != nil {
			return err
		}
		seen[item.ID] = struct{}{}
	}
	missingIDs := make([]int64, 0)
	for i := range existing {
		if _, ok := seen[existing[i].ID]; !ok {
			missingIDs = append(missingIDs, existing[i].ID)
		}
	}
	if len(missingIDs) == 0 {
		return nil
	}
	return tx.Where("id IN ?", missingIDs).Delete(&models.Library{}).Error
}

func ConvertBytes(bytes float64) string {
	if bytes == 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	log := math.Log(bytes) / math.Log(1024)
	unitIndex := int(log)
	if unitIndex >= len(units) {
		unitIndex = len(units) - 1
	}
	return fmt.Sprintf("%.2f %s", bytes/math.Pow(1024, float64(unitIndex)), units[unitIndex])
}

func (s *MysqlOP) CreateLibrary(lb *models.Library) error {
	lib := s.Lib
	s.Lib = ""
	if err := validateMySQLIdentifier(lib, 64); err != nil {
		return err
	}
	if err := validateMySQLIdentifier(lb.User, 32); err != nil {
		return fmt.Errorf("invalid database username: %w", err)
	}
	charset, collation, err := mysqlCharset(lb.Encoding)
	if err != nil {
		return err
	}
	err = s.Connect()
	if err != nil {
		return err
	}
	defer s.Close()
	var databaseCount int64
	if err := s.DB.Raw(
		"SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?",
		lib,
	).Scan(&databaseCount).Error; err != nil {
		return err
	}
	if databaseCount > 0 {
		return fmt.Errorf("database %s already exists", lib)
	}
	var userCount int64
	if err := s.DB.Raw(
		"SELECT COUNT(*) FROM mysql.user WHERE User = ? AND Host = ?",
		lb.User,
		managedMySQLAccountHost,
	).Scan(&userCount).Error; err != nil {
		return err
	}
	if userCount > 0 {
		return fmt.Errorf(
			"database user %s@%s already exists",
			lb.User,
			managedMySQLAccountHost,
		)
	}
	sqlDB, err := s.DB.DB()
	if err != nil {
		return err
	}
	quotedDatabase := quoteMySQLIdentifier(lib)
	_, err = sqlDB.Exec(
		"CREATE DATABASE " + quotedDatabase +
			" CHARACTER SET " + charset +
			" COLLATE " + collation,
	)
	if err != nil {
		return err
	}
	createdUser := false
	cleanup := func() {
		if createdUser {
			_, _ = sqlDB.Exec(
				"DROP USER IF EXISTS ?@?",
				lb.User,
				managedMySQLAccountHost,
			)
		}
		_, _ = sqlDB.Exec("DROP DATABASE IF EXISTS " + quotedDatabase)
	}
	_, err = sqlDB.Exec(
		"CREATE USER ?@? IDENTIFIED BY ?",
		lb.User,
		managedMySQLAccountHost,
		lb.Password,
	)
	if err != nil {
		cleanup()
		return err
	}
	createdUser = true

	_, err = sqlDB.Exec(
		"GRANT ALL PRIVILEGES ON "+quotedDatabase+".* TO ?@?",
		lb.User,
		managedMySQLAccountHost,
	)
	if err != nil {
		cleanup()
		return err
	}
	credentialCheck := &MysqlOP{
		Addr:     s.Addr,
		Port:     s.Port,
		Root:     lb.User,
		Password: lb.Password,
		Type:     s.Type,
		Lib:      lib,
	}
	if err := credentialCheck.Connect(); err != nil {
		cleanup()
		return fmt.Errorf("verify database user credential: %w", err)
	}
	if err := credentialCheck.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close database user credential check: %w", err)
	}
	log.Println("Database, user, and permissions created successfully.")
	return nil
}

func (s *MysqlOP) DeleteLibrary(lb *models.Library) error {
	if err := validateMySQLIdentifier(lb.Name, 64); err != nil {
		return err
	}
	if lb.User != "" {
		if err := validateMySQLIdentifier(lb.User, 32); err != nil {
			return fmt.Errorf("invalid database username: %w", err)
		}
	}
	s.Lib = ""
	if err := s.Connect(); err != nil {
		return err
	}
	defer s.Close()
	sqlDB, err := s.DB.DB()
	if err != nil {
		return err
	}
	if _, err := sqlDB.Exec("DROP DATABASE " + quoteMySQLIdentifier(lb.Name)); err != nil {
		return err
	}
	if lb.User != "" {
		if _, err := sqlDB.Exec(
			"DROP USER IF EXISTS ?@?",
			lb.User,
			managedMySQLAccountHost,
		); err != nil {
			return fmt.Errorf("database removed but user cleanup failed: %w", err)
		}
	}
	return nil
}

func (s *MysqlOP) UpdateLibraryPassword(lb *models.Library, password string) error {
	if err := validateMySQLIdentifier(lb.User, 32); err != nil {
		return fmt.Errorf("invalid database username: %w", err)
	}
	s.Lib = ""
	if err := s.Connect(); err != nil {
		return err
	}
	defer s.Close()
	sqlDB, err := s.DB.DB()
	if err != nil {
		return err
	}
	if _, err := sqlDB.Exec(
		"ALTER USER ?@? IDENTIFIED BY ?",
		lb.User,
		managedMySQLAccountHost,
		password,
	); err != nil {
		return fmt.Errorf("update database user password: %w", err)
	}
	credentialCheck := &MysqlOP{
		Addr:     s.Addr,
		Port:     s.Port,
		Root:     lb.User,
		Password: password,
		Type:     s.Type,
		Lib:      lb.Name,
	}
	if err := credentialCheck.Connect(); err != nil {
		return fmt.Errorf("verify updated database user credential: %w", err)
	}
	if err := credentialCheck.Close(); err != nil {
		return fmt.Errorf("close updated database user credential check: %w", err)
	}
	return nil
}

var mysqlIdentifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func validateMySQLIdentifier(value string, maxLength int) error {
	if len(value) < 1 || len(value) > maxLength || !mysqlIdentifierPattern.MatchString(value) {
		return fmt.Errorf("identifier must start with a letter and contain only letters, numbers, or underscores")
	}
	return nil
}

func quoteMySQLIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func mysqlCharset(value string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "utf8", "utf8mb4":
		return "utf8mb4", "utf8mb4_unicode_ci", nil
	case "utf8mb3":
		return "utf8mb3", "utf8mb3_unicode_ci", nil
	case "gbk":
		return "gbk", "gbk_chinese_ci", nil
	case "big5":
		return "big5", "big5_chinese_ci", nil
	default:
		return "", "", fmt.Errorf("unsupported database encoding: %s", value)
	}
}
