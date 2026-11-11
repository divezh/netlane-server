package geoip

import "github.com/oschwald/maxminddb-golang"

// openDatabase opens a MaxMind-format country database. It is a separate function
// so the rest of the package can be tested without a real database file.
func openDatabase(path string) (reader, error) {
	return maxminddb.Open(path)
}
