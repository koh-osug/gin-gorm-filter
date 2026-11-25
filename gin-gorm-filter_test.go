// Copyright (c) 2022 ActiveChooN
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package filter

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type Base struct {
	ID        uint      `gorm:"primarykey"`
	CreatedAt time.Time `filter:"sortable"`
	UpdatedAt time.Time
}

type User struct {
	Base
	Username string `filter:"param:login;searchable;filterable"`
	FullName string `gorm:"column:name" filter:"searchable"`
	Email    string `filter:"filterable"`
	Age      int    `filter:"filterable"`
	Active   bool   `filter:"searchable,filterable"`
	GroupID  *uint  `filter:"filterable"`
	// Not exposed.
	Password string
	Group    *Group
}

type Group struct {
	ID   uint
	Name string
}

var userColumns = []string{"id", "created_at", "updated_at", "username", "name", "email", "age", "active", "group_id", "password"}

type TestSuite struct {
	suite.Suite
	db   *gorm.DB
	mock sqlmock.Sqlmock
}

func (s *TestSuite) SetupTest() {
	var (
		db  *sql.DB
		err error
	)

	db, s.mock, err = sqlmock.New()
	s.NoError(err)

	dialector := postgres.New(postgres.Config{
		DSN:                  "sqlmock_db_0",
		DriverName:           "postgres",
		Conn:                 db,
		PreferSimpleProtocol: true,
	})

	s.db, err = gorm.Open(dialector, &gorm.Config{})
	require.NoError(s.T(), err)
}

func (s *TestSuite) TearDownTest() {
	s.NoError(s.mock.ExpectationsWereMet())
	db, err := s.db.DB()
	require.NoError(s.T(), err)
	db.Close()
}

func context(rawQuery string) *gin.Context {
	return &gin.Context{Request: &http.Request{URL: &url.URL{RawQuery: rawQuery}}}
}

// expect runs the query for rawQuery and checks the SQL and its arguments.
func (s *TestSuite) expect(rawQuery string, config int, sqlRegexp string, args ...interface{}) {
	var users []User
	query := s.mock.ExpectQuery(sqlRegexp)
	if len(args) > 0 {
		query = query.WithArgs(toDriverValues(args)...)
	}
	query.WillReturnRows(sqlmock.NewRows(userColumns))
	err := s.db.Model(&User{}).Scopes(FilterByQuery(context(rawQuery), config)).Find(&users).Error
	s.NoError(err, rawQuery)
}

// refuse runs the query for rawQuery and checks that it fails with ErrInvalidQuery without
// reaching the database.
func (s *TestSuite) refuse(rawQuery string, config int) {
	var users []User
	err := s.db.Model(&User{}).Scopes(FilterByQuery(context(rawQuery), config)).Find(&users).Error
	s.Error(err, rawQuery)
	s.True(errors.Is(err, ErrInvalidQuery), "%s: %v", rawQuery, err)
	var qe *QueryError
	s.True(errors.As(err, &qe), rawQuery)
}

func toDriverValues(args []interface{}) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a
	}
	return out
}

func (s *TestSuite) TestFilterEqual() {
	s.expect("filter=login:sampleUser", ALL,
		`^SELECT \* FROM "users" WHERE "username" = \$1 ORDER BY "id" DESC LIMIT \$2$`,
		"sampleUser", 10)
}

func (s *TestSuite) TestFilterByColumnName() {
	s.expect("filter=username:sampleUser", FILTER,
		`^SELECT \* FROM "users" WHERE "username" = \$1$`, "sampleUser")
}

func (s *TestSuite) TestFilterLike() {
	s.expect("filter=login~samp", FILTER,
		`^SELECT \* FROM "users" WHERE "username" LIKE \$1$`, "samp")
}

func (s *TestSuite) TestFilterOperators() {
	s.expect("filter=age!=22", FILTER, `^SELECT \* FROM "users" WHERE "age" <> \$1$`, int64(22))
	s.expect("filter=age<=200", FILTER, `^SELECT \* FROM "users" WHERE "age" <= \$1$`, int64(200))
	s.expect("filter=age>=99", FILTER, `^SELECT \* FROM "users" WHERE "age" >= \$1$`, int64(99))
	s.expect("filter=age>100", FILTER, `^SELECT \* FROM "users" WHERE "age" > \$1$`, int64(100))
	s.expect("filter=login<Phil", FILTER, `^SELECT \* FROM "users" WHERE "username" < \$1$`, "Phil")
}

func (s *TestSuite) TestFilterTypedValues() {
	s.expect("filter=active:true", FILTER, `^SELECT \* FROM "users" WHERE "active" = \$1$`, true)
	s.expect("filter=group_id:7", FILTER, `^SELECT \* FROM "users" WHERE "group_id" = \$1$`, uint64(7))
}

func (s *TestSuite) TestFilterSeveralConditions() {
	s.expect("filter=login:sampleUser&filter=email:john@example.com", FILTER,
		`^SELECT \* FROM "users" WHERE "username" = \$1 AND "email" = \$2$`,
		"sampleUser", "john@example.com")
	s.expect("filter=age:3,active:false,", FILTER,
		`^SELECT \* FROM "users" WHERE "age" = \$1 AND "active" = \$2$`,
		int64(3), false)
}

func (s *TestSuite) TestFilterRefused() {
	for _, q := range []string{
		"filter=password:secret",    // column without filterable
		"filter=Password:secret",    // field name, not a column
		"filter=unknown:1",          // no such column
		"filter=age:abc",            // not an int
		"filter=group_id:-1",        // not a uint
		"filter=active:maybe",       // not a bool
		"filter=age~1",              // LIKE on a number
		"filter=login",              // no operator
		"filter=login:a,password:b", // one bad condition fails all
		"filter=x login:a",          // not a column name
	} {
		s.refuse(q, FILTER)
	}
}

func (s *TestSuite) TestNoFilterConfig() {
	s.expect("filter=login:sampleUser", SEARCH, `^SELECT \* FROM "users"$`)
}

func (s *TestSuite) TestSearch() {
	// Only the searchable text columns; active is tagged searchable but is a bool.
	s.expect("search=John", SEARCH,
		`^SELECT \* FROM "users" WHERE \(LOWER\("username"\) LIKE \$1 OR LOWER\("name"\) LIKE \$2\)$`,
		"%john%", "%john%")
}

func (s *TestSuite) TestSearchAndFilter() {
	s.expect("filter=login:sampleUser&search=John", FILTER|SEARCH,
		`^SELECT \* FROM "users" WHERE \(LOWER\("username"\) LIKE \$1 OR LOWER\("name"\) LIKE \$2\) AND "username" = \$3$`,
		"%john%", "%john%", "sampleUser")
}

func (s *TestSuite) TestPaginate() {
	s.expect("page=2&page_size=10", ALL,
		`^SELECT \* FROM "users" ORDER BY "id" DESC LIMIT \$1 OFFSET \$2$`, 10, 10)
	s.expect("page_size=1000", PAGINATE, `^SELECT \* FROM "users" LIMIT \$1$`, 100)
	s.expect("all=true", PAGINATE, `^SELECT \* FROM "users"$`)
}

func (s *TestSuite) TestPaginateRefused() {
	s.refuse("page=abc", PAGINATE)
	s.refuse("page=9999999999", PAGINATE)
}

func (s *TestSuite) TestOrderBy() {
	s.expect("order_by=email&order_direction=asc", ORDER_BY, `^SELECT \* FROM "users" ORDER BY "email"$`)
	s.expect("order_by=login", ORDER_BY, `^SELECT \* FROM "users" ORDER BY "username" DESC$`)
	s.expect("order_by=created_at", ORDER_BY, `^SELECT \* FROM "users" ORDER BY "created_at" DESC$`)
	s.expect("", ORDER_BY, `^SELECT \* FROM "users" ORDER BY "id" DESC$`)
}

func (s *TestSuite) TestOrderByRefused() {
	for _, q := range []string{
		"order_by=password",   // not sortable
		"order_by=name",       // searchable only
		"order_by=updated_at", // not tagged
		"order_by=Email",      // not a column name
		"order_by=1",          // not a column
		"order_by=group",      // a relation
		"order_by=email&order_direction=up",
	} {
		s.refuse(q, ORDER_BY)
	}
}

func (s *TestSuite) TestColumnsOf() {
	c, err := ColumnsOf(s.db, &User{})
	s.NoError(err)
	s.Equal([]string{"login", "name"}, c.Searchable)
	s.Equal([]string{"active", "age", "email", "group_id", "login"}, c.Filterable)
	s.Equal([]string{"active", "age", "created_at", "email", "group_id", "id", "login"}, c.Sortable)
}

func TestRunSuite(t *testing.T) {
	suite.Run(t, new(TestSuite))
}
