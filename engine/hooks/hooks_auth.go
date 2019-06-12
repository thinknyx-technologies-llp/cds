package hooks

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/ovh/cds/engine/service"
	"github.com/ovh/cds/sdk"
	"github.com/ovh/cds/sdk/cdsclient"
)

func (s *Service) authMiddleware(ctx context.Context, w http.ResponseWriter, req *http.Request, rc *service.HandlerConfig) (context.Context, error) {
	if !rc.NeedAuth {
		return ctx, nil
	}

	hash, err := base64.StdEncoding.DecodeString(req.Header.Get(cdsclient.AuthHeader))
	if err != nil {
		return ctx, fmt.Errorf("bad header syntax: %s", err)
	}

	if s.Hash == string(hash) {
		return ctx, nil
	}

	return ctx, sdk.ErrUnauthorized
}
