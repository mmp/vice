module github.com/mmp/vice/pkg/util

go 1.22.4

require (
	github.com/hugolgst/rich-go v0.0.0-20230917173849-4a4fb1d3c362
	github.com/iancoleman/orderedmap v0.3.0
	github.com/klauspost/compress v1.17.9
	github.com/mmp/vice/pkg/log v0.0.0-00010101000000-000000000000
	github.com/mmp/vice/pkg/math v0.0.0
	golang.org/x/exp v0.0.0-20240613232115-7f521ea00fb8
)

require (
	github.com/MichaelTJones/pcg v0.0.0-20180122055547-df440c6ed7ed // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/natefinch/npipe.v2 v2.0.0-20160621034901-c1b8fa8bdcce // indirect
)

replace github.com/mmp/vice/pkg/math => ../math

replace github.com/mmp/vice/pkg/log => ../log

replace github.com/mmp/vice/pkg/rand => ../rand
