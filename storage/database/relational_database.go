package database

import (
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jinzhu/gorm"
	"github.com/klaytn/klaytn/log"
	_ "github.com/mattn/go-sqlite3"
	"strings"
	"time"
)

type KeyValueModel struct {
	Id  int    `gorm:"column:id;type:INT AUTO_INCREMENT;PRIMARY_KEY;NOT NULL"`
	Key []byte `gorm:"column:binkey;type:VARBINARY(100);UNIQUE_INDEX;NOT NULL"`
	Val []byte `gorm:"column:binval;type:MEDIUMBLOB"`
}

const mysqlDialect = "mysql"
const sqliteDialect = "sqlite3"

type rdb struct {
	db           *gorm.DB
	sqlDB        *sql.DB
	preparedStmt *sql.Stmt
	logger       log.Logger
}

func newRelationalDatabase(endpoint, dialect string) (*rdb, error) {
	var db *gorm.DB
	var sqlDB *sql.DB
	var err error

	switch dialect {
	case mysqlDialect:
		id := "root"
		password := "rootroot"
		endpoint = fmt.Sprintf("%s:%s@tcp(kes-melvin-test-mysql.cnuopt13avbx.ap-northeast-2.rds.amazonaws.com:3306)/mysql", id, password)
		logger.Info("endpoint", "ep", endpoint)
		db, err = openMySQL(endpoint)
		setMySQLDatabase(db)

		endpoint2 := fmt.Sprintf("%s:%s@tcp(kes-melvin-test-mysql.cnuopt13avbx.ap-northeast-2.rds.amazonaws.com:3306)/test", id, password)
		db, err = openMySQL(endpoint2)
		if err := db.Exec("USE test").Error; err != nil {
			return nil, err
		}

		sqlDB, err = sql.Open("mysql", endpoint2)
		if err != nil {
			logger.Error("failed to open database", "err", err)
		}
		_, err = sqlDB.Exec("USE test")
		if err != nil {
			logger.Error("failed to set database", "err", err)
		}

	case sqliteDialect:
		db, err = gorm.Open("sqlite3", ":memory:")
	default:
		return nil, fmt.Errorf("%w - given dialect: %s", notSupportedDialectErr, dialect)
	}

	if err != nil {
		return nil, err
	}

	//db.LogMode(true)

	logger.Info("")
	if err := db.Exec("USE test").Error; err != nil {
		logger.Error("Failed to use test", "err", err)
	}

	err = db.AutoMigrate(&KeyValueModel{}).Error
	if err != nil {
		return nil, err
	}

	return &rdb{db: db, sqlDB: sqlDB, logger: logger.NewWith("", "")}, nil
}

func openMySQL(endpoint string) (*gorm.DB, error) {
	var db *gorm.DB
	var err error
	for i := 0; i < 5; i++ {
		db, err = gorm.Open("mysql", endpoint)
		if err == nil {
			return db, nil
		}

		if strings.Contains(err.Error(), "connect: connection refused") {
			logger.Info("sleep for a while and retry connecting to db", "endpoint", endpoint)
			time.Sleep(1 * time.Second)
		} else {
			logger.Error("failed to connect to database", "tried", i+1, "err", err)
		}
	}

	return db, err
}

func setMySQLDatabase(mysql *gorm.DB) error {
	//Drop previous test database if possible.
	//if err := mysql.Exec("DROP DATABASE test").Error; err != nil {
	//	if !strings.Contains(err.Error(), "database doesn't exist") {
	//		logger.Error("Error while dropping the database test", "err", err)
	//	}
	//}
	//// Create new test database.
	if err := mysql.Exec("CREATE DATABASE test DEFAULT CHARACTER SET UTF8").Error; err != nil {
		logger.Error("Error while creating database", "err", err)
	}
	// Use test database
	if err := mysql.Exec("USE test").Error; err != nil {
		return err
	}

	//if err := mysql.Exec("SET profiling = 1").Error; err != nil {
	//	return err
	//}
	return nil
}

const mysqlPutQuery = `
			INSERT /*+ SET_VAR(performance_schema = ON) */ INTO test.key_value_models(binkey, binval)
			VALUES (?, ?) 
			ON DUPLICATE KEY UPDATE binval=values(binval)`

const mysqlBatchQuery = `
			INSERT /*+ SET_VAR(performance_schema = ON) */ INTO test.key_value_models(binkey, binval)
			VALUES %s 
			ON DUPLICATE KEY UPDATE binkey=values(binkey), binval=values(binval)`

const sqlitePutQuery = `
			INSERT INTO test.key_value_models(binkey, binval)
			VALUES (?, ?)
			ON CONFLICT (binkey)
			DO
			UPDATE SET binval=excluded.binval`

const sqliteBatchQuery = `
			INSERT INTO test.key_value_models(binkey, binval)
			VALUES %s
			ON CONFLICT (binkey)
			DO
			UPDATE SET binval=excluded.binval`

var notSupportedDialectErr = errors.New("given dialect is not supported")

func (rdb *rdb) Put(key []byte, val []byte) error {
	switch rdb.db.Dialect().GetName() {
	case mysqlDialect:
		return rdb.db.Exec(mysqlPutQuery, key, val).Error
	case sqliteDialect:
		return rdb.db.Exec(sqlitePutQuery, key, val).Error
	default:
		return fmt.Errorf("%w - given dialect: %s", notSupportedDialectErr, rdb.db.Dialect().GetName())
	}
}

func (rdb *rdb) Get(key []byte) ([]byte, error) {
	var result KeyValueModel
	if err := rdb.db.Where(&KeyValueModel{Key: key}).Take(&result).Error; err != nil {
		return nil, err
	}
	return result.Val, nil
}

func (rdb *rdb) Has(key []byte) (bool, error) {
	if val, err := rdb.Get(key); val != nil && err == nil {
		return true, nil
	} else {
		return false, err
	}
}

func (rdb *rdb) Delete(key []byte) error {
	return rdb.db.Delete(&KeyValueModel{Key: key}).Error
}

func (rdb *rdb) Close() {
	if err := rdb.db.Close(); err != nil {
		rdb.logger.Error("error while closing relational database", "err", err)
	} else {
		rdb.logger.Info("successfully closed relational database")
	}
}

func (rdb *rdb) NewBatch() Batch {
	return &rdbBatch{
		db:         rdb.db,
		rdb:        rdb,
		batchItems: []*KeyValueModel{},
		size:       0,
	}
}

func (rdb *rdb) Type() DBType {
	return RelationalDB
}

func (rdb *rdb) Meter(prefix string) {
	// does nothing
}

type rdbBatch struct {
	db         *gorm.DB
	rdb        *rdb
	batchItems []*KeyValueModel
	size       int
}

func (b *rdbBatch) Put(key, val []byte) error {
	b.batchItems = append(b.batchItems, &KeyValueModel{Key: key, Val: val})
	b.size += len(val)
	return nil
}

func (b *rdbBatch) Write() error {
	if b.size == 0 {
		return nil
	}
	start := time.Now()
	defer func() {
		logger.Info("BatchWriteTotal", "elapsed", time.Since(start), "size", b.size, "numItems", len(b.batchItems))
	}()

	var placeholders []string
	var queryArgs []interface{}

	numItems := 0

	//if err := b.db.Exec("ALTER TABLE test.key_value_models DISABLE KEYS").Error; err != nil {
	//	logger.Error("Error while altering table", "err", err)
	//	return err
	//}
	//
	//defer func() {
	//	if err := b.db.Exec("ALTER TABLE test.key_value_models ENABLE KEYS").Error; err != nil {
	//		logger.Error("Error while altering table", "err", err)
	//	}
	//}()

	maxBatchSize := 2000
	writeSize := 0

	for _, item := range b.batchItems {
		numItems++

		placeholders = append(placeholders, "(?,?)")
		queryArgs = append(queryArgs, item.Key)
		queryArgs = append(queryArgs, item.Val)
		writeSize += len(item.Val)

		if numItems >= maxBatchSize {
			if b.rdb.preparedStmt == nil {
				concatenatedPlaceholders := strings.Join(placeholders, ",")
				query := fmt.Sprintf(mysqlBatchQuery, concatenatedPlaceholders)

				stmt, err := b.rdb.sqlDB.Prepare(query)
				if err != nil {
					logger.Error("Error while prepare query", "err", err)
				}
				b.rdb.preparedStmt = stmt
			}

			batchWriteStart := time.Now()
			_, err := b.rdb.preparedStmt.Exec(queryArgs...)
			if err != nil {
				logger.Error("Error while batch write", "err", err)
				return err
			}

			writeTime := time.Since(batchWriteStart)
			logger.Info("BatchWrite 2000 items", "elapsed", writeTime, "numItems", numItems, "writeSize", writeSize)
			relationalDBBatchWriteMeter.Mark(int64(writeTime / 2000.0))

			placeholders = []string{}
			queryArgs = []interface{}{}
			numItems = 0
			writeSize = 0
		}
	}

	if numItems == 0 {
		return nil
	}

	startRemaining := time.Now()
	defer func() {
		writeRemainingTime := time.Since(startRemaining)
		logger.Info("BatchWrite Remainings", "elapsed", writeRemainingTime, "numItems", numItems)
		relationalDBBatchWriteMeter.Mark(int64(writeRemainingTime / 2000.0))
	}()
	var query string
	switch b.db.Dialect().GetName() {
	case mysqlDialect:
		query = fmt.Sprintf(mysqlBatchQuery, strings.Join(placeholders, ","))
	case sqliteDialect:
		query = fmt.Sprintf(sqliteBatchQuery, strings.Join(placeholders, ","))
	default:
		return fmt.Errorf("%w - given dialect: %s", notSupportedDialectErr, b.db.Dialect().GetName())
	}

	return b.db.Exec(query, queryArgs...).Error
}

func (b *rdbBatch) genPlaceholdersAndArgs() (string, []interface{}) {
	// TODO Below can be replaced by simple 'Create` when upgrading to gorm v2
	var placeholders []string
	var queryArgs []interface{}

	for _, item := range b.batchItems {
		placeholders = append(placeholders, "(?,?)")

		queryArgs = append(queryArgs, item.Key)
		queryArgs = append(queryArgs, item.Val)
	}

	return strings.Join(placeholders, ","), queryArgs
}

func (b *rdbBatch) ValueSize() int {
	return b.size
}

func (b *rdbBatch) Reset() {
	b.size = 0
	b.batchItems = []*KeyValueModel{}
}