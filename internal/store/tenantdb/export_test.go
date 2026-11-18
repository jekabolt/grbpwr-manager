package tenantdb

import "database/sql/driver"

// NewConnectorOver builds the routing connector over any driver connector (tests: a fake driver).
func NewConnectorOver(inner driver.Connector, homeDB string, o Options) (*Connector, error) {
	return newConnector(inner, homeDB, o)
}
