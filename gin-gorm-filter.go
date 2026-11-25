// Copyright (c) 2021 ActiveChooN
//
// This software is released under the MIT License.
// https://opensource.org/licenses/MIT

package filter

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

type queryParams struct {
	Search         string   `form:"search"`
	Filter         []string `form:"filter"`
	Page           int      `form:"page,default=1"`
	PageSize       int      `form:"page_size,default=10"`
	All            bool     `form:"all,default=false"`
	OrderBy        string   `form:"order_by"`
	OrderDirection string   `form:"order_direction,default=desc"`
}

const (
	SEARCH   = 1  // Filter response with LIKE query "search={search_phrase}"
	FILTER   = 2  // Filter response by column name values "filter={column_name}:{value}"
	PAGINATE = 4  // Paginate response with page and page_size
	ORDER_BY = 8  // Order response by column name
	ALL      = 15 // Equivalent to SEARCH|FILTER|PAGINATE|ORDER_BY
	tagKey   = "filter"

	maxPageSize     = 100
	defaultPageSize = 10
	maxValueInError = 64
)

// ErrInvalidQuery is matched (errors.Is) by every error caused by the query parameters of the
// request: a column that is not allowed, a value of the wrong type, a malformed parameter. A
// handler answers it with 400.
var ErrInvalidQuery = errors.New("invalid query parameter")

// QueryError describes a refused query parameter. It matches ErrInvalidQuery.
type QueryError struct {
	Param  string // the query parameter, e.g. "order_by"
	Value  string // the refused value, shortened
	Reason string
}

func (e *QueryError) Error() string {
	return fmt.Sprintf("%s: %s=%q: %s", ErrInvalidQuery, e.Param, e.Value, e.Reason)
}

func (e *QueryError) Unwrap() error {
	return ErrInvalidQuery
}

func queryError(param, value, reason string) *QueryError {
	if len(value) > maxValueInError {
		value = value[:maxValueInError] + "…"
	}
	return &QueryError{Param: param, Value: value, Reason: reason}
}

// filterPartRegexp splits one filter condition into name, operator and value. The compound
// operators come first so that ">=" is not read as ">" followed by a value starting with "=".
var filterPartRegexp = regexp.MustCompile(`^(\w+)(:|!=|>=|<=|>|<|~)(.*)$`)

// field is a model field with its filter options.
type field struct {
	param      string // name in the query: the `param:` option, or else the column name
	column     string
	dataType   schema.DataType
	searchable bool
	filterable bool
	sortable   bool
}

// options parses the `filter` tag. Options are separated by ";" (or ",").
func options(tag string) (opts map[string]bool, param string) {
	opts = map[string]bool{}
	for _, opt := range strings.FieldsFunc(tag, func(r rune) bool { return r == ';' || r == ',' }) {
		opt = strings.TrimSpace(opt)
		if strings.HasPrefix(opt, "param:") {
			param = strings.TrimPrefix(opt, "param:")
			continue
		}
		opts[opt] = true
	}
	return opts, param
}

// modelFields reads the fields of the model from GORM's schema, so that the column names are the
// ones GORM uses (naming strategy, `column:` tag) and fields of embedded structs are included.
// Fields without a column (relations, `gorm:"-"`) are left out.
func modelFields(db *gorm.DB, model interface{}) ([]field, error) {
	if model == nil {
		return nil, errors.New("filter: no model; call db.Model(&Model{}) before the scope")
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}
	fields := make([]field, 0, len(stmt.Schema.Fields))
	for _, f := range stmt.Schema.Fields {
		if f.DBName == "" {
			continue
		}
		opts, param := options(f.Tag.Get(tagKey))
		if param == "" {
			param = f.DBName
		}
		fields = append(fields, field{
			param:      param,
			column:     f.DBName,
			dataType:   f.DataType,
			searchable: opts["searchable"] && f.DataType == schema.String,
			filterable: opts["filterable"],
			// Filtering with < and > already compares values, so sorting by a filterable
			// column discloses nothing more.
			sortable: f.PrimaryKey || opts["sortable"] || opts["filterable"],
		})
	}
	return fields, nil
}

// lookup returns the field named name in the query, or else the field with the column name, for
// which allowed holds.
func lookup(fields []field, name string, allowed func(field) bool) (field, bool) {
	for _, f := range fields {
		if allowed(f) && f.param == name {
			return f, true
		}
	}
	for _, f := range fields {
		if allowed(f) && f.column == name {
			return f, true
		}
	}
	return field{}, false
}

// Columns lists the query names of the fields of a model that can be searched, filtered and
// sorted by.
type Columns struct {
	Searchable []string
	Filterable []string
	Sortable   []string
}

// ColumnsOf returns the Columns of model. Use it in tests to pin what a model exposes.
func ColumnsOf(db *gorm.DB, model interface{}) (Columns, error) {
	fields, err := modelFields(db, model)
	if err != nil {
		return Columns{}, err
	}
	var c Columns
	for _, f := range fields {
		if f.searchable {
			c.Searchable = append(c.Searchable, f.param)
		}
		if f.filterable {
			c.Filterable = append(c.Filterable, f.param)
		}
		if f.sortable {
			c.Sortable = append(c.Sortable, f.param)
		}
	}
	sort.Strings(c.Searchable)
	sort.Strings(c.Filterable)
	sort.Strings(c.Sortable)
	return c, nil
}

func isSortable(f field) bool { return f.sortable }

// orderBy sorts by order_by, by default by "id". A model without a sortable "id" is not sorted
// by default.
func orderBy(db *gorm.DB, fields []field, params queryParams) error {
	var desc bool
	switch params.OrderDirection {
	case "desc":
		desc = true
	case "asc":
	default:
		return queryError("order_direction", params.OrderDirection, "must be asc or desc")
	}
	var f field
	var ok bool
	if params.OrderBy == "" {
		if f, ok = lookup(fields, "id", isSortable); !ok {
			return nil
		}
	} else if f, ok = lookup(fields, params.OrderBy, isSortable); !ok {
		return queryError("order_by", params.OrderBy, "not a sortable column")
	}
	db.Statement.AddClause(clause.OrderBy{Columns: []clause.OrderByColumn{{
		Column: clause.Column{Name: f.column},
		Desc:   desc,
	}}})
	return nil
}

func paginate(db *gorm.DB, params queryParams) error {
	if params.All {
		return nil
	}
	if params.Page < 1 {
		params.Page = 1
	}
	switch {
	case params.PageSize > maxPageSize:
		params.PageSize = maxPageSize
	case params.PageSize <= 0:
		params.PageSize = defaultPageSize
	}
	if params.Page-1 > math.MaxInt32/params.PageSize {
		return queryError("page", strconv.Itoa(params.Page), "too large")
	}
	offset := (params.Page - 1) * params.PageSize
	db.Statement.AddClause(clause.Limit{Limit: &params.PageSize, Offset: offset})
	return nil
}

// value converts a filter value to the type of the column, so that a wrong value is refused here
// instead of failing in the database.
func value(f field, raw string) (interface{}, error) {
	switch f.dataType {
	case schema.Bool:
		return strconv.ParseBool(raw)
	case schema.Int:
		return strconv.ParseInt(raw, 10, 64)
	case schema.Uint:
		return strconv.ParseUint(raw, 10, 64)
	case schema.Float:
		return strconv.ParseFloat(raw, 64)
	case schema.Time:
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return t, nil
		}
		return time.Parse(time.DateOnly, raw)
	default:
		return raw, nil
	}
}

func filterCondition(fields []field, part string) (clause.Expression, error) {
	m := filterPartRegexp.FindStringSubmatch(part)
	if m == nil {
		return nil, queryError("filter", part, "expected {column}{operator}{value}, operator one of : != >= <= > < ~")
	}
	name, op, raw := m[1], m[2], m[3]
	f, ok := lookup(fields, name, func(f field) bool { return f.filterable })
	if !ok {
		return nil, queryError("filter", part, "not a filterable column")
	}
	column := clause.Column{Name: f.column}
	if op == "~" {
		if f.dataType != schema.String {
			return nil, queryError("filter", part, "~ applies to text columns only")
		}
		return clause.Like{Column: column, Value: raw}, nil
	}
	v, err := value(f, raw)
	if err != nil {
		return nil, queryError("filter", part, fmt.Sprintf("not a valid %s value", f.dataType))
	}
	switch op {
	case ">=":
		return clause.Gte{Column: column, Value: v}, nil
	case "<=":
		return clause.Lte{Column: column, Value: v}, nil
	case "!=":
		return clause.Neq{Column: column, Value: v}, nil
	case ">":
		return clause.Gt{Column: column, Value: v}, nil
	case "<":
		return clause.Lt{Column: column, Value: v}, nil
	default:
		return clause.Eq{Column: column, Value: v}, nil
	}
}

// filter ANDs every condition. A filter parameter holds one or more conditions separated by ",".
func filter(db *gorm.DB, fields []field, filters []string) error {
	var expressions []clause.Expression
	for _, phrase := range filters {
		for _, part := range strings.Split(phrase, ",") {
			if part == "" {
				continue
			}
			expression, err := filterCondition(fields, part)
			if err != nil {
				return err
			}
			expressions = append(expressions, expression)
		}
	}
	if len(expressions) > 0 {
		db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.And(expressions...)}})
	}
	return nil
}

// search ORs a case-insensitive LIKE over the searchable text columns.
func search(db *gorm.DB, fields []field, phrase string) {
	pattern := "%" + strings.ToLower(phrase) + "%"
	var expressions []clause.Expression
	for _, f := range fields {
		if f.searchable {
			expressions = append(expressions, clause.Expr{
				SQL:  "LOWER(?) LIKE ?",
				Vars: []interface{}{clause.Column{Name: f.column}, pattern},
			})
		}
	}
	if len(expressions) > 0 {
		db.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Or(expressions...)}})
	}
}

// FilterByQuery filters, sorts and paginates a DB request with the query parameters of the
// request. The model must be set first:
//
//	db.Model(&UserModel{}).Scopes(filter.FilterByQuery(ctx, filter.ALL)).Find(&users)
//
// Or if only pagination and order is needed:
//
//	db.Model(&UserModel{}).Scopes(filter.FilterByQuery(ctx, filter.PAGINATE|filter.ORDER_BY)).Find(&users)
//
// The `filter` tag of a field opts it in:
//
//		type User struct {
//			gorm.Model
//			Username string `gorm:"uniqueIndex" filter:"param:login;searchable;filterable"`
//			// `param` defines the name of the column in the query
//			FullName string `filter:"searchable"`
//			Rank     int    `filter:"sortable"`
//		}
//
//	  - searchable: matched by search= (text columns only)
//	  - filterable: usable in filter= and order_by=
//	  - sortable: usable in order_by=; the primary key always is
//
// Any other column in filter= or order_by=, a value of the wrong type or a malformed parameter
// fails the query with an error matching ErrInvalidQuery, and nothing is sent to the database.
func FilterByQuery(c *gin.Context, config int) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		var params queryParams
		if err := c.ShouldBindQuery(&params); err != nil {
			_ = db.AddError(queryError("query", c.Request.URL.RawQuery, err.Error()))
			return db
		}
		fields, err := modelFields(db, db.Statement.Model)
		if err != nil {
			_ = db.AddError(err)
			return db
		}
		if config&SEARCH > 0 && params.Search != "" {
			search(db, fields, params.Search)
		}
		if config&FILTER > 0 && len(params.Filter) > 0 {
			if err := filter(db, fields, params.Filter); err != nil {
				_ = db.AddError(err)
				return db
			}
		}
		if config&ORDER_BY > 0 {
			if err := orderBy(db, fields, params); err != nil {
				_ = db.AddError(err)
				return db
			}
		}
		if config&PAGINATE > 0 {
			if err := paginate(db, params); err != nil {
				_ = db.AddError(err)
				return db
			}
		}
		return db
	}
}
