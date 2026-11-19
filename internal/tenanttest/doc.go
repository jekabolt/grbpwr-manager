// Package tenanttest is the hostile isolation matrix of teams mode: every AdminService method is
// called through the production edge (tenantbind binder → auth admin chain → generated handler)
// as one team while naming, presenting or reading another, against a real control plane and real
// team databases (test MySQL through test-db.sh). It holds tests only.
package tenanttest
