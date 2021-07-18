module github.com/jekabolt/grbpwr-manager

go 1.16

require (
	github.com/caarlos0/env/v6 v6.6.2
	github.com/go-chi/chi v4.1.2+incompatible
	github.com/go-chi/httprate v0.5.0
	github.com/go-chi/render v1.0.1
	github.com/go-ini/ini v1.62.0 // indirect
	github.com/go-kivik/couchdb/v4 v4.0.0-20200818191020-c997633e0a27
	github.com/jdeng/goheif v0.0.0-20200323230657-a0d6a8b3e68f
	github.com/minio/minio-go v6.0.14+incompatible
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/rs/cors v1.7.0
	github.com/rs/zerolog v1.19.0
	github.com/rwcarlsen/goexif v0.0.0-20190401172101-9e8deecbddbd // indirect
	github.com/smartystreets/goconvey v1.6.4 // indirect
	github.com/tidwall/buntdb v1.2.4
	github.com/tidwall/gjson v1.8.0
	golang.org/x/net v0.0.0-20200707034311-ab3426394381 // indirect
	golang.org/x/text v0.3.2 // indirect
	gopkg.in/ini.v1 v1.62.0 // indirect
)

//replace gitlab.com/dvision/go-cri => ../../dvision/go-cri
