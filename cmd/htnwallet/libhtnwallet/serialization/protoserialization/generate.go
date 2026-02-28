//go:generate protoc --go_out=. --go_opt=paths=source_relative  --go-vtproto_out=. --go-vtproto_opt=paths=source_relative,pool=truesize=true wallet.proto

package protoserialization
