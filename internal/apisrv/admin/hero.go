package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) AddHero(ctx context.Context, req *pb_admin.AddHeroRequest) (*pb_admin.AddHeroResponse, error) {
	if err := s.validateHeroEmbeds(req.Hero); err != nil {
		slog.Default().WarnContext(ctx, "rejected hero with invalid embed", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	heroFull := dto.ConvertCommonHeroFullInsertToEntity(req.Hero)

	err := s.repo.Hero().SetHero(ctx, heroFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't add hero")
	}

	s.revalidateAsync(&dto.RevalidationData{
		Hero: true,
	})
	return &pb_admin.AddHeroResponse{}, nil
}

// validateEmbedURL enforces the shared iframe embed policy: an absolute https
// URL whose host is in the allowlist (when one is configured). Shared by hero
// and archive embed validation so the policy has a single source of truth.
func (s *Server) validateEmbedURL(embedURL string) error {
	u, err := url.Parse(embedURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return errors.New("embed_url must be an absolute https URL")
	}
	if len(s.embedAllowedHosts) > 0 && !hostAllowed(u.Hostname(), s.embedAllowedHosts) {
		return fmt.Errorf("embed host %q is not in the allowlist", u.Hostname())
	}
	return nil
}

// validateHeroEmbeds ensures every EMBED block references a safe iframe source.
func (s *Server) validateHeroEmbeds(h *pb_common.HeroFullInsert) error {
	if h == nil {
		return nil
	}
	for i, e := range h.Entities {
		if e.Type != pb_common.HeroType_HERO_TYPE_EMBED || e.Embed == nil {
			continue
		}
		if err := s.validateEmbedURL(e.Embed.EmbedUrl); err != nil {
			return fmt.Errorf("hero entity %d: %w", i, err)
		}
	}
	return nil
}

// parseEmbedAllowedHosts splits a comma-separated host allowlist into a
// normalized (trimmed, lowercased, non-empty) slice.
func parseEmbedAllowedHosts(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	hosts := make([]string, 0, len(parts))
	for _, p := range parts {
		if h := strings.ToLower(strings.TrimSpace(p)); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// hostAllowed reports whether host exactly matches, or is a subdomain of, any
// allowed host.
func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(host)
	for _, a := range allowed {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}
