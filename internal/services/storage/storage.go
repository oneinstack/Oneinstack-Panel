package storage

import (
	"context"
	"errors"
	"fmt"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"oneinstack/router/output"
	"oneinstack/utils"
	"strings"
	"time"

	"gorm.io/gorm"
)

func Add(param *input.AddParam) error {
	normalizeConnectionParam(param)
	s := &models.Storage{}
	tx := app.DB().Where("addr = ? and port = ? and type = ?", param.Addr, param.Port, param.Type).First(s)
	if tx.Error != nil && !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
		return tx.Error
	}
	if s.ID > 0 {
		return fmt.Errorf("%s:%v 已存在", param.Addr, param.Port)
	}
	m := &models.Storage{
		Addr:     param.Addr,
		Port:     param.Port,
		Root:     param.Root,
		Password: param.Password,
		Remark:   param.Remark,
		Type:     param.Type,
	}
	if err := testStorageConnection(m); err != nil {
		return fmt.Errorf("database connection test failed: %w", err)
	}
	encrypted, err := utils.EncryptCredential(
		m.Password,
		utils.CredentialPurposeStoragePassword,
	)
	if err != nil {
		return err
	}
	m.Password = encrypted
	tx = app.DB().Create(m)
	return tx.Error
}

func List(ty string) ([]output.StorageConnection, error) {
	list := []*models.Storage{}
	tx := app.DB()
	if ty != "" && ty != "all" {
		tx = tx.Where("type = ?", ty)
	}
	tx = tx.Find(&list)
	if tx.Error != nil && !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
		return nil, tx.Error
	}
	if ty == "mysql" && len(list) == 0 {
		restored, restoreErr := ensureRecordedLocalMySQLConnection()
		if restoreErr != nil {
			return nil, restoreErr
		}
		if restored != nil {
			list = append(list, restored)
		}
	}
	result := make([]output.StorageConnection, 0, len(list))
	for _, item := range list {
		result = append(result, storageConnectionOutput(item))
	}
	return result, nil
}

func LibList(param *input.QueryParam) (*services.PaginatedResult[output.DatabaseLibrary], error) {
	if param.Type == "redis" {
		return GetRedisLib(param)
	}
	query := app.DB().Where("type = ?", param.Type)
	if param.ID > 0 {
		query = query.Where("p_id = ?", param.ID)
	}
	if name := strings.TrimSpace(param.Name); name != "" {
		query = query.Where("name LIKE ?", "%"+name+"%")
	}
	result, err := services.Paginate[models.Library](
		query,
		&models.Library{},
		&input.Page{
			Page:     param.Page.Page,
			PageSize: param.Page.PageSize,
		})
	if err != nil {
		return nil, err
	}
	return libraryPageOutput(result), nil
}

func GetRedisLib(param *input.QueryParam) (*services.PaginatedResult[output.DatabaseLibrary], error) {
	s, err := loadStorage(param.ID)
	if err != nil {
		return nil, err
	}
	if s.Type != "redis" {
		return nil, fmt.Errorf("connection %d is not a Redis connection", s.ID)
	}
	op := NewRedisOP(s)
	if err := op.Connect(); err != nil {
		return nil, err
	}
	defer op.Close()
	libs, err := op.GetLibs()
	if err != nil {
		return nil, err
	}
	data := make([]output.DatabaseLibrary, 0, len(libs))
	for i := range libs {
		data = append(data, libraryOutput(&libs[i]))
	}
	return &services.PaginatedResult[output.DatabaseLibrary]{
		Data:       data,
		Total:      len(libs),
		Page:       1,
		PageSize:   100,
		TotalPages: 1,
	}, nil
}

func AddLibs(param *input.LibParam) (*output.DatabaseCredential, error) {
	if param.ID <= 0 {
		return nil, fmt.Errorf("未检测到可用 MySQL 实例，请先安装或修复 MySQL")
	}
	s, err := loadStorage(param.ID)
	if err != nil {
		return nil, err
	}
	if s.Type != "mysql" {
		return nil, fmt.Errorf("creating logical databases is supported only for MySQL connections")
	}
	param.Root = param.Name
	if strings.TrimSpace(param.Password) == "" {
		param.Password, err = utils.GenerateSecurePassword(24)
		if err != nil {
			return nil, err
		}
	}
	if err := input.ValidateDatabaseUserPassword(param.Password); err != nil {
		return nil, err
	}
	var existing int64
	if err := app.DB().Model(&models.Library{}).
		Where("p_id = ? AND name = ?", s.ID, param.Name).
		Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, fmt.Errorf("database %s already exists in Panel", param.Name)
	}
	op, err := newStorageOP(s, param.Name)
	if err != nil {
		return nil, err
	}
	lb := &models.Library{
		PID:        s.ID,
		Name:       param.Name,
		User:       param.Root,
		Password:   param.Password,
		Encoding:   param.Encoding,
		Capacity:   "",
		PAddr:      fmt.Sprintf("%s:%v", s.Addr, s.Port),
		Type:       s.Type,
		CreateTime: time.Now(),
	}
	if err := op.CreateLibrary(lb); err != nil {
		return nil, err
	}
	encrypted, err := utils.EncryptCredential(
		lb.Password,
		utils.CredentialPurposeLibraryPassword,
	)
	if err != nil {
		_ = op.DeleteLibrary(lb)
		return nil, err
	}
	lb.Password = encrypted
	if err := app.DB().Create(lb).Error; err != nil {
		lb.Password = param.Password
		rollbackErr := op.DeleteLibrary(lb)
		if rollbackErr != nil {
			return nil, fmt.Errorf("save database metadata: %v; remote rollback failed: %w", err, rollbackErr)
		}
		return nil, fmt.Errorf("save database metadata: %w", err)
	}
	return &output.DatabaseCredential{
		LibraryID: lb.ID,
		Database:  lb.Name,
		Username:  lb.User,
		Password:  param.Password,
	}, nil
}

func Del(param *input.IDParam) error {
	return app.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("p_id = ?", param.ID).Delete(&models.Library{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Storage{}, param.ID).Error
	})
}

func Sync(param *input.IDParam) error {
	m, err := loadStorage(param.ID)
	if err != nil {
		return err
	}
	op, err := newStorageOP(m, "information_schema")
	if err != nil {
		return err
	}
	if err := op.Connect(); err != nil {
		return err
	}
	defer op.Close()
	return op.Sync()
}

func Update(param *input.AddParam) error {
	normalizeConnectionParam(param)
	s, err := loadStorage(param.ID)
	if err != nil {
		return err
	}
	s.Addr = param.Addr
	s.Port = param.Port
	s.Root = param.Root
	if param.Password != "" {
		s.Password = param.Password
	}
	s.Remark = param.Remark
	s.Type = param.Type
	if err := testStorageConnection(s); err != nil {
		return fmt.Errorf("database connection test failed: %w", err)
	}
	encrypted, err := utils.EncryptCredential(
		s.Password,
		utils.CredentialPurposeStoragePassword,
	)
	if err != nil {
		return err
	}
	return app.DB().Model(&models.Storage{}).
		Where("id = ?", s.ID).
		Updates(map[string]any{
			"addr":        s.Addr,
			"port":        s.Port,
			"root":        s.Root,
			"password":    encrypted,
			"remark":      s.Remark,
			"type":        s.Type,
			"update_time": time.Now(),
		}).Error
}

func RedisKeyList(param *input.QueryParam) (*PaginatedKeysInfo, error) {
	s, err := loadStorage(param.ID)
	if err != nil {
		return nil, err
	}
	if s.Type != "redis" {
		return nil, fmt.Errorf("connection %d is not a Redis connection", s.ID)
	}
	op := NewRedisOP(s)
	if err := op.Connect(); err != nil {
		return nil, err
	}
	defer op.Close()
	return op.GetPaginatedKeyInfo(context.Background(), param.RDB, "", param.Page.Page, param.PageSize)
}

func CheckStorage() (bool, bool) {
	// 检查是否安装了Mysql
	mysql := &models.Software{}
	mysqlTx := app.DB().Model(&models.Software{}).Where("`key` = ? AND installed = ?", "db", 1).First(mysql)
	mysqlInstalled := mysqlTx.Error == nil && mysqlTx.RowsAffected > 0

	// 检查是否安装了Redis
	redis := &models.Software{}
	redisTx := app.DB().Model(&models.Software{}).Where("`key` = ? AND installed = ?", "redis", 1).First(redis)
	redisInstalled := redisTx.Error == nil && redisTx.RowsAffected > 0

	return mysqlInstalled, redisInstalled
}

func DeleteLibrary(param *input.DeleteLibraryParam) error {
	var library models.Library
	if err := app.DB().First(&library, param.ID).Error; err != nil {
		return err
	}
	if strings.TrimSpace(param.ConfirmName) != library.Name {
		return fmt.Errorf("database confirmation name does not match")
	}
	if isSystemDatabase(library.Name) {
		return fmt.Errorf("system database %s cannot be deleted", library.Name)
	}
	connection, err := loadStorage(library.PID)
	if err != nil {
		return err
	}
	if connection.Type != "mysql" {
		return fmt.Errorf("deleting Redis logical databases is not supported")
	}
	op, err := newStorageOP(connection, "")
	if err != nil {
		return err
	}
	if err := op.DeleteLibrary(&library); err != nil {
		return err
	}
	return app.DB().Delete(&models.Library{}, library.ID).Error
}

func TestConnection(param *input.AddParam) error {
	normalizeConnectionParam(param)
	candidate := &models.Storage{
		Addr: param.Addr, Port: param.Port, Root: param.Root,
		Password: param.Password, Remark: param.Remark, Type: param.Type,
	}
	if param.ID > 0 && candidate.Password == "" {
		existing, err := loadStorage(param.ID)
		if err != nil {
			return err
		}
		candidate.Password = existing.Password
	}
	return testStorageConnection(candidate)
}

// EnsureManagedLocalMySQLConnection records the root credential generated for
// a fresh Panel-managed MySQL installation. Existing local connections are
// preserved so upgrades never overwrite an administrator's credential.
func EnsureManagedLocalMySQLConnection(port, username, password string) error {
	port = strings.TrimSpace(port)
	if port == "" {
		port = "3306"
	}
	username = strings.TrimSpace(username)
	if username == "" {
		username = "root"
	}
	if username != "root" || port != "3306" {
		return fmt.Errorf("managed local MySQL must use root on port 3306")
	}
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("managed local MySQL credential is empty")
	}
	var existing models.Storage
	result := app.DB().
		Where("type = ? AND port = ? AND addr IN ?", "mysql", port, []string{"127.0.0.1", "localhost"}).
		First(&existing)
	if result.Error == nil {
		return nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return result.Error
	}
	candidate := &models.Storage{
		Addr:     "127.0.0.1",
		Port:     port,
		Root:     username,
		Password: password,
		Remark:   "本机 MySQL（面板自动管理）",
		Type:     "mysql",
	}
	if err := testStorageConnection(candidate); err != nil {
		return fmt.Errorf("verify managed local MySQL connection: %w", err)
	}
	encrypted, err := utils.EncryptCredential(
		password,
		utils.CredentialPurposeStoragePassword,
	)
	if err != nil {
		return err
	}
	candidate.Password = encrypted
	return app.DB().Create(candidate).Error
}

func GetLibraryCredential(id int64) (*output.DatabaseCredential, error) {
	var library models.Library
	if id <= 0 {
		return nil, fmt.Errorf("database is required")
	}
	if err := app.DB().First(&library, id).Error; err != nil {
		return nil, err
	}
	if library.Type != "mysql" || strings.TrimSpace(library.User) == "" {
		return nil, fmt.Errorf("database does not have a managed MySQL account")
	}
	password, err := utils.DecryptCredential(
		library.Password,
		utils.CredentialPurposeLibraryPassword,
	)
	if err != nil {
		return nil, fmt.Errorf("decrypt database user credential: %w", err)
	}
	return &output.DatabaseCredential{
		LibraryID: library.ID,
		Database:  library.Name,
		Username:  library.User,
		Password:  password,
	}, nil
}

func UpdateLibraryCredential(id int64, password string) (*output.DatabaseCredential, error) {
	var err error
	password = strings.TrimSpace(password)
	if password == "" {
		password, err = utils.GenerateSecurePassword(24)
		if err != nil {
			return nil, err
		}
	}
	if err := input.ValidateDatabaseUserPassword(password); err != nil {
		return nil, err
	}
	var library models.Library
	if id <= 0 {
		return nil, fmt.Errorf("database is required")
	}
	if err := app.DB().First(&library, id).Error; err != nil {
		return nil, err
	}
	if library.Type != "mysql" || strings.TrimSpace(library.User) == "" {
		return nil, fmt.Errorf("database does not have a managed MySQL account")
	}
	oldPassword, err := utils.DecryptCredential(
		library.Password,
		utils.CredentialPurposeLibraryPassword,
	)
	if err != nil {
		return nil, fmt.Errorf("decrypt current database user credential: %w", err)
	}
	connection, err := loadStorage(library.PID)
	if err != nil {
		return nil, err
	}
	op, err := newStorageOP(connection, "")
	if err != nil {
		return nil, err
	}
	if err := op.UpdateLibraryPassword(&library, password); err != nil {
		if oldPassword != "" {
			_ = op.UpdateLibraryPassword(&library, oldPassword)
		}
		return nil, err
	}
	encrypted, err := utils.EncryptCredential(
		password,
		utils.CredentialPurposeLibraryPassword,
	)
	if err != nil {
		if oldPassword != "" {
			_ = op.UpdateLibraryPassword(&library, oldPassword)
		}
		return nil, err
	}
	if err := app.DB().Model(&models.Library{}).
		Where("id = ?", library.ID).
		Update("password", encrypted).Error; err != nil {
		if oldPassword != "" {
			_ = op.UpdateLibraryPassword(&library, oldPassword)
		}
		return nil, fmt.Errorf("save database user credential: %w", err)
	}
	return &output.DatabaseCredential{
		LibraryID: library.ID,
		Database:  library.Name,
		Username:  library.User,
		Password:  password,
	}, nil
}

func testStorageConnection(storage *models.Storage) error {
	lib := ""
	if storage.Type == "mysql" {
		lib = "information_schema"
	}
	op, err := newStorageOP(storage, lib)
	if err != nil {
		return err
	}
	if err := op.Connect(); err != nil {
		return err
	}
	return op.Close()
}

func loadStorage(id int64) (*models.Storage, error) {
	if id <= 0 {
		return nil, fmt.Errorf("database connection is required")
	}
	var storage models.Storage
	if err := app.DB().First(&storage, id).Error; err != nil {
		return nil, err
	}
	if storage.Password != "" {
		password, err := utils.DecryptCredential(
			storage.Password,
			utils.CredentialPurposeStoragePassword,
		)
		if err != nil {
			return nil, fmt.Errorf("decrypt database connection credential: %w", err)
		}
		storage.Password = password
	}
	return &storage, nil
}

func normalizeConnectionParam(param *input.AddParam) {
	param.Addr = strings.TrimSpace(param.Addr)
	param.Port = strings.TrimSpace(param.Port)
	param.Root = strings.TrimSpace(param.Root)
	param.Remark = strings.TrimSpace(param.Remark)
	param.Type = strings.ToLower(strings.TrimSpace(param.Type))
}

func storageConnectionOutput(item *models.Storage) output.StorageConnection {
	return output.StorageConnection{
		ID: item.ID, Addr: item.Addr, Port: item.Port, Root: item.Root,
		Remark: item.Remark, Type: item.Type,
		PasswordConfigured: item.Password != "",
		Managed:            strings.Contains(item.Remark, "面板自动管理"),
		CreateTime:         item.CreateTime, UpdateTime: item.UpdateTime,
	}
}

func ensureRecordedLocalMySQLConnection() (*models.Storage, error) {
	var installed models.Software
	if err := app.DB().
		Where("`key` = ? AND installed = ?", "db", true).
		Order("install_time DESC").
		First(&installed).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	password := strings.TrimSpace(installed.RootPwd)
	if password == "" {
		return nil, nil
	}
	if err := EnsureManagedLocalMySQLConnection("3306", "root", password); err != nil {
		return nil, nil
	}
	var restored models.Storage
	if err := app.DB().
		Where("type = ? AND port = ? AND addr = ?", "mysql", "3306", "127.0.0.1").
		First(&restored).Error; err != nil {
		return nil, err
	}
	return &restored, nil
}

func libraryOutput(item *models.Library) output.DatabaseLibrary {
	return output.DatabaseLibrary{
		ID: item.ID, PID: item.PID, Name: item.Name, User: item.User,
		PasswordConfigured: item.Password != "", Encoding: item.Encoding,
		Capacity: item.Capacity, PAddr: item.PAddr, Type: item.Type,
		CreateTime: item.CreateTime,
	}
}

func libraryPageOutput(
	page *services.PaginatedResult[models.Library],
) *services.PaginatedResult[output.DatabaseLibrary] {
	data := make([]output.DatabaseLibrary, 0, len(page.Data))
	for i := range page.Data {
		data = append(data, libraryOutput(&page.Data[i]))
	}
	return &services.PaginatedResult[output.DatabaseLibrary]{
		Data: data, Total: page.Total, Page: page.Page,
		PageSize: page.PageSize, TotalPages: page.TotalPages,
	}
}

func isSystemDatabase(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "information_schema", "mysql", "performance_schema", "sys":
		return true
	default:
		return false
	}
}
