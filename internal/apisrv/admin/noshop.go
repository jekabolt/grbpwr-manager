package admin

// SetNoShop marks an installation without a shop (TENANT_STOREFRONT_TEAM_ID=none): stock writes
// and receipts skip their storefront side effects — back-in-stock letters and ISR revalidation.
// The stock itself is written as always.
func (s *Server) SetNoShop(on bool) {
	s.noShop = on
}
