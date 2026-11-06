module cryptochat/integration

go 1.22

require (
	cryptochat/core v0.0.0
	cryptochat/server v0.0.0
	github.com/rabbitmq/amqp091-go v1.16.0
	google.golang.org/grpc v1.67.3
)

require (
	golang.org/x/net v0.28.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
	golang.org/x/text v0.21.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240814211410-ddb44dafa142 // indirect
	google.golang.org/protobuf v1.34.2 // indirect
)

replace (
	cryptochat/core => ../core
	cryptochat/server => ../server
)
