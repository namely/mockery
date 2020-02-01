package test

import (
	"github.com/namely/mockery/mockery/fixtures/http"
)

type HasConflictingNestedImports interface {
	RequesterNS
	Z() http.MyStruct
}
