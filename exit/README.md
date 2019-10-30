## Build
Simply run:
```shell
go build bin/main.go
```

## How to use
Edit `config.json` with your data:
```json
{
  "ListenTCP": 30004,
  "ListenUDP": 30005,
  "Reverse": false,
  "ReverseRandomPorts": false,
  "DialTimeout": 30,
  "UDPTimeout": 60,
  "Seed": "",
  "SubscriptionPrefix": "tuna+1.",
  "SubscriptionDuration": 60,
  "SubscriptionFee": "0",
  "ClaimInterval": 60,
  "Services": {
    "httpproxy": {
      "address": "127.0.0.1",
      "price": "0.001"
    },
    "moonlight": {
      "address": "127.0.0.1",
      "price": "0.001"
    }
  }
}
```
`ListenTCP` TCP port to listen for connections  
`ListenUDP` UDP port to listen for connections  
`Reverse` should be used if you don't have public IP and want to use another `server` for accepting clients  
`ReverseRandomPorts` meaning reverse entry can use random ports instead of specified ones (useful when service has dynamic ports)  
`DialTimeout` timeout for connections to services  
`UDPTimeout`  timeout for UDP *connections*  
`Seed` your seed  
`SubscriptionPrefix` prefix appended to topics for subscription  
`SubscriptionDuration` duration for subscription in blocks  
`SubscriptionFee` fee used for subscription  
`ClaimInterval` payment claim interval for connections  
`Services` services you want to provide  

Run like this:
```shell
./bin/main
```

Then users can connect to your services over NKN through their *tuna* client

## Specifying service ports programmatically
In case you're running services with dynamic ports it may be inconvenient to specify port in the `services.json` before each launch.  
Instead you can run TUNA's exit programmatically, launch your service and then provide it's port to TUNA.

```go
...
// read/prepare config
// init wallet sdk
// launch your service
port := ... // port of the newly launched service
...
serviceName := "proxy"
services := []Service{{
    Name: serviceName,
    TCP:  []int{port},
}}
exit := NewTunaExit(config, services, wallet) // can be used only once-per-service
exit.StartReverse(serviceName)

select {} // prevent TUNA from exiting
```

## Getting dynamic ports
When some of the ports of the service is specified as 0, then entry will use random ports for them, here's how you can check which ports it's actually using:
```go
exit.OnEntryConnected(func() {
    ip := exit.GetReverseIP()
    tcpPorts := exit.GetReverseTCPPorts()
    udpPorts := exit.GetReverseUDPPorts()
})
```
Returned ports will correspond to local ports specified in the same order.  
This information will only be available after connection with the reverse entry has been made.