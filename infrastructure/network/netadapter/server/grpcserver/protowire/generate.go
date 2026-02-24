//go:generate protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative --go-vtproto_out=. --go-vtproto_opt=paths=source_relative,pool=truesize=true p2p.proto rpc.proto messages.proto

package protowire
