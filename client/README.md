## Build
Simply run:
```shell
glide install
go build client.go
```

## How to use
Edit `config.json` with your data:
```json
{
  "DialTimeout": 30,
  "PrivateKey": "",
  "Services": ["httpproxy", "moonlight"]
}
```
`DialTimeout` timeout for NKN node connection  
`PrivateKey` your private key  
`Services` services you want to use  

Run like this:
```shell
./client
```

Then you can start using configured services as if they're on your local machine (e.g. `127.0.0.1:8888` for HTTP proxy)
