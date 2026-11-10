// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"fmt"
	"net/http"
	"strings"

	"gitea.dev/modules/setting"
	"gitea.dev/services/context"
	files_service "gitea.dev/services/repository/files"
)

// GetBlob get the blob of a repository file.
func GetBlob(ctx *context.APIContext) {
	// swagger:operation GET /repos/{owner}/{repo}/git/blobs/{sha} repository GetBlob
	// ---
	// summary: Gets the blob of a repository.
	// produces:
	// - application/json
	// parameters:
	// - name: owner
	//   in: path
	//   description: owner of the repo
	//   type: string
	//   required: true
	// - name: repo
	//   in: path
	//   description: name of the repo
	//   type: string
	//   required: true
	// - name: sha
	//   in: path
	//   description: sha of the commit
	//   type: string
	//   required: true
	// responses:
	//   "200":
	//     "$ref": "#/responses/GitBlobResponse"
	//   default:
	//     "$ref": "#/responses/error"

	sha := ctx.PathParam("sha")
	if len(sha) == 0 {
		ctx.APIError(http.StatusBadRequest, "sha not provided")
		return
	}

	if blob, err := files_service.GetBlobBySHA(ctx, ctx.Repo.Repository, ctx.Repo.GitRepo, sha); err != nil {
		ctx.APIError(http.StatusBadRequest, err.Error())
	} else {
		ctx.JSON(http.StatusOK, blob)
	}
}

// GetBlobs gets several blobs of a repository
func GetBlobs(ctx *context.APIContext) {
	// swagger:operation GET /repos/{owner}/{repo}/git/blobs repository GetBlobs
	// ---
	// summary: Gets several blobs of a repository.
	// description: Returns the blobs in the requested order, null for SHAs that are not blobs in the repository.
	//              Blobs that are too large, or that would make the response too large, are returned without content,
	//              they can be requested separately.
	// produces:
	// - application/json
	// parameters:
	// - name: owner
	//   in: path
	//   description: owner of the repo
	//   type: string
	//   required: true
	// - name: repo
	//   in: path
	//   description: name of the repo
	//   type: string
	//   required: true
	// - name: shas
	//   in: query
	//   description: comma-separated SHAs of the blobs, at most the maximum number of response items
	//   type: string
	//   required: true
	// responses:
	//   "200":
	//     "$ref": "#/responses/GitBlobList"
	//   default:
	//     "$ref": "#/responses/error"

	shas := strings.Split(ctx.FormTrim("shas"), ",")
	if shas[0] == "" {
		ctx.APIError(http.StatusBadRequest, "shas not provided")
		return
	}
	if len(shas) > setting.API.MaxResponseItems {
		ctx.APIError(http.StatusBadRequest, fmt.Sprintf("at most %d shas can be requested", setting.API.MaxResponseItems))
		return
	}

	blobs, err := files_service.GetBlobsBySHA(ctx, ctx.Repo.Repository, ctx.Repo.GitRepo, shas)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, blobs)
}
