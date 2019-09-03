## Build
Simply run:
```shell
glide install
go build entry.go
```

## How to use
Edit `config.json` with your data:
```json
{
  "DialTimeout": 30,
  "UDPTimeout": 60,
  "Seed": "",
  "Services": {
    "httpproxy": {
      "maxPrice": "0.001"
    },
    "moonlight": {
      "maxPrice": "0.001"
    }
  },
  "Reverse": false,
  "ReverseTCP": 40004,
  "ReverseUDP": 40005,
  "ReversePrice": "0.001",
  "ReverseClaimInterval": 60,
  "SubscriptionPrefix": "tuna+1.",
  "SubscriptionDuration": 60
}
```
`DialTimeout` timeout for NKN node connection  
`UDPTimeout`  timeout for UDP *connections*  
`Seed` your seed  
`Services` services you want to use  

`Reverse` should be used to provide reverse tunnel for those who don't have public IP  
`ReverseTCP` TCP port to listen for connections  
`ReverseUDP` UDP port to listen for connections  
`ReversePrice` price for reverse connections  
`ReverseClaimInterval` payment claim interval for reverse connections  
`SubscriptionPrefix` prefix appended to topics for subscription  
`SubscriptionDuration` duration for subscription in blocks  

Run like this:
```shell
./entry
```

Then you can start using configured services as if they're on your local machine (e.g. `127.0.0.1:8888` for HTTP proxy)
