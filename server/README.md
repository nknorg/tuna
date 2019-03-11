## Build
Simply run:
```shell
glide install
go build server.go
```

## How to use
Edit `config.json` with your data:
```json
{
  "ListenTCP": 30004,
  "ListenUDP": 30005,
  "DialTimeout": 30,
  "PrivateKey": "",
  "SubscriptionDuration": 60,
  "SubscriptionInterval": 20,
  "Services": ["httpproxy", "moonlight"]
}
```
`ListenTCP` TCP port to listen for connections  
`ListenUDP` UDP port to listen for connections  
`DialTimeout` timeout for connections to services  
`UDPTimeout`  timeout for UDP *connections*  
`PrivateKey` your private key  
`SubscriptionDuration` duration for subscription in blocks  
`SubscriptionInterval` interval for subscription in seconds  
`Services` services you want to provide  

Run like this:
```shell
./server
```

Then users can connect to your services over NKN through their *tuna* client
