<!--
 Copyright (c) 2021 ActiveCHooN

 This software is released under the MIT License.
 https://opensource.org/licenses/MIT
-->

# Gin GORM filter
![GitHub](https://img.shields.io/github/license/ActiveChooN/gin-gorm-filter)
![GitHub Workflow Status (branch)](https://img.shields.io/github/actions/workflow/status/ActiveChooN/gin-gorm-filter/ci.yml?branch=master)
![GitHub release (latest by date)](https://img.shields.io/github/v/release/ActiveChooN/gin-gorm-filter)

Scope function for GORM queries provides easy filtering with query parameters

## Usage

```(shell)
go get github.com/ActiveChooN/gin-gorm-filter
```

## Model definition
```go
type UserModel struct {
    gorm.Model
    Username string `gorm:"uniqueIndex" filter:"param:login;searchable;filterable"`
    FullName string `filter:"searchable"`
    Role     string `filter:"filterable"`
    Rank     int    `filter:"sortable"`
    Password string // not usable in any query parameter
}
```
The `filter` tag opts a field in:

| Option | Effect |
| --- | --- |
| `searchable` | matched by `search=` (case-insensitive `LIKE`, text columns only) |
| `filterable` | usable in `filter=` and in `order_by=` |
| `sortable` | usable in `order_by=` |
| `param:<name>` | the name of the field in the query; by default its column name |

The primary key is always sortable; `order_by` defaults to `id`. Column names come from GORM's
schema, so `column:` tags, the naming strategy and embedded structs such as `gorm.Model` are
taken into account.

Anything else is refused before the query reaches the database: a column in `filter=` or
`order_by=` that is not allowed, a filter value that does not parse as the column's type
(integer, unsigned, float, bool, time in RFC 3339 or `YYYY-MM-DD`), `~` on a column that is not
text, an `order_direction` other than `asc`/`desc`, or a malformed parameter. The query then
fails with an error matching `filter.ErrInvalidQuery` (a `*filter.QueryError`); answer it with
400. `filter.ColumnsOf(db, &Model{})` lists what a model exposes, for a test.

## Controller Example
```go
func GetUsers(c *gin.Context) {
	var users []UserModel
	var usersCount int64
	db, err := gorm.Open(sqlite.Open("gorm.db"), &gorm.Config{})
	err := db.Model(&UserModel{}).Scopes(
		filter.FilterByQuery(c, filter.ALL),
	).Count(&usersCount).Find(&users).Error
	if errors.Is(err, filter.ErrInvalidQuery) {
		c.JSON(http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, "internal error")
		return
	}
	serializer := serializers.PaginatedUsers{Users: users, Count: usersCount}
	c.JSON(http.StatusOK, serializer.Response())
}
```
Any filter combination can be used here `filter.PAGINATE|filter.ORDER_BY` e.g. **Important note:** GORM model should be initialize first for DB, otherwise filter and search won't work

## Request example
```(shell)
curl -X GET http://localhost:8080/users?page=1&limit=10&order_by=username&order_direction=asc&filter="name:John"
```

## Supported filter operators
- :   The equality operator `filter=username:John` matches only when the username is exactly `John`
- \>  The greater than operator `filter=age>35` matches only when age is more than 35
- \<  The less than operator `filter=salary<80000` matches only when salary is less than 80,000
- \>= The greater than or equals to operator `filter=items>=100` matches only when items is at least 100
- \<= The less than or equals to operator `filter=score<=100000` matches when score is 100,000 or lower
- \!= The not equals to operator `state!=FAIL` matches when state has any value other than FAIL
- \~  The like operator `filter=lastName~%illi%` matches when lastName contains the substring `illi` (the value is the `LIKE` pattern)

Several conditions can be given in one parameter, separated by `,` (`filter=role:admin,rank>3`),
or in several `filter` parameters; all of them must hold.

## TODO list
- [x] Write tests for the lib with CI integration
- [x] Add support for case-insensitive search
- [x] Add other filters, like > or !=
- [x] Add support for multiple filters in one query
- [ ] Add suport for filtering for related models
