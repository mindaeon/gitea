// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	auth_model "gitea.dev/models/auth"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unittest"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/setting"
	api "gitea.dev/modules/structs"
	"gitea.dev/modules/test"
	"gitea.dev/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIReposGitBlobsList(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	const (
		readmeSHA  = "4b4851ad51df6a7d9f25c979345979eaeb5b349f"
		commitSHA  = "65f1bf27bc3bf70f64657658635e66094edbcb4d"
		treeSHA    = "2a2f1d4670728a2e10049e345bd7a276468beab6"
		unknownSHA = "1234567890123456789012345678901234567890"
		zeroSHA    = "0000000000000000000000000000000000000000"
		readmeB64  = "IyByZXBvMQoKRGVzY3JpcHRpb24gZm9yIHJlcG8x"
	)
	getBlobs := func(t *testing.T, repoName, shas string, token ...string) []*api.GitBlobResponse {
		req := NewRequest(t, "GET", "/api/v1/repos/user2/"+repoName+"/git/blobs?shas="+url.QueryEscape(shas))
		if len(token) > 0 {
			req.AddTokenAuth(token[0])
		}
		return *DecodeJSON(t, MakeRequest(t, req, http.StatusOK), &[]*api.GitBlobResponse{})
	}

	blobs := getBlobs(t, "repo1", strings.Join([]string{readmeSHA, commitSHA, treeSHA, unknownSHA, zeroSHA, "master:README.md", readmeSHA}, ","))
	require.Len(t, blobs, 7)
	require.NotNil(t, blobs[0])
	assert.Equal(t, readmeSHA, blobs[0].SHA)
	assert.EqualValues(t, 30, blobs[0].Size)
	assert.Equal(t, readmeB64, *blobs[0].Content)
	assert.Equal(t, []*api.GitBlobResponse{nil, nil, nil, nil, nil}, blobs[1:6])
	assert.Equal(t, blobs[0], blobs[6])

	t.Run("ResponseSize", func(t *testing.T) {
		defer test.MockVariableValue(&setting.API.DefaultMaxResponseSize, 40)()
		blobs := getBlobs(t, "repo1", readmeSHA+","+readmeSHA)
		require.Len(t, blobs, 2)
		assert.NotNil(t, blobs[0].Content)
		assert.EqualValues(t, 30, blobs[1].Size)
		assert.Nil(t, blobs[1].Content)
	})

	t.Run("Cap", func(t *testing.T) {
		defer test.MockVariableValue(&setting.API.MaxResponseItems, 2)()
		assert.Len(t, getBlobs(t, "repo1", readmeSHA+","+readmeSHA), 2)
		MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/user2/repo1/git/blobs?shas="+strings.Repeat(readmeSHA+",", 2)+readmeSHA), http.StatusBadRequest)
		MakeRequest(t, NewRequest(t, "GET", "/api/v1/repos/user2/repo1/git/blobs"), http.StatusBadRequest)
	})

	t.Run("Permissions", func(t *testing.T) {
		const repo16ReadmeSHA = "f90451c72ef61a7645293d17b47be7a8e983da57"
		privateURL := "/api/v1/repos/user2/repo16/git/blobs?shas=" + repo16ReadmeSHA
		MakeRequest(t, NewRequest(t, "GET", privateURL), http.StatusNotFound)
		MakeRequest(t, NewRequest(t, "GET", privateURL).AddTokenAuth(getUserToken(t, "user2", auth_model.AccessTokenScopeReadUser)), http.StatusForbidden)
		MakeRequest(t, NewRequest(t, "GET", privateURL).AddTokenAuth(getUserToken(t, "user4", auth_model.AccessTokenScopeReadRepository)), http.StatusNotFound)

		blobs := getBlobs(t, "repo16", repo16ReadmeSHA+","+readmeSHA, getUserToken(t, "user2", auth_model.AccessTokenScopeReadRepository))
		require.Len(t, blobs, 2)
		assert.Equal(t, repo16ReadmeSHA, blobs[0].SHA)
		assert.Nil(t, blobs[1], "a blob of another repository")
	})
}

func TestAPIReposGitBlobs(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})         // owner of the repo1 & repo16
	org3 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 3})          // owner of the repo3
	user4 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})         // owner of neither repos
	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})   // public repo
	repo3 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 3})   // public repo
	repo16 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 16}) // private repo
	repo1ReadmeSHA := "65f1bf27bc3bf70f64657658635e66094edbcb4d"
	repo3ReadmeSHA := "d56a3073c1dbb7b15963110a049d50cdb5db99fc"
	repo16ReadmeSHA := "f90451c72ef61a7645293d17b47be7a8e983da57"
	badSHA := "0000000000000000000000000000000000000000"

	// Login as User2.
	session := loginUser(t, user2.Name)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeReadRepository)

	// Test a public repo that anyone can GET the blob of
	req := NewRequestf(t, "GET", "/api/v1/repos/%s/%s/git/blobs/%s", user2.Name, repo1.Name, repo1ReadmeSHA)
	resp := MakeRequest(t, req, http.StatusOK)
	gitBlobResponse := DecodeJSON(t, resp, &api.GitBlobResponse{})
	assert.NotNil(t, gitBlobResponse)
	expectedContent := "dHJlZSAyYTJmMWQ0NjcwNzI4YTJlMTAwNDllMzQ1YmQ3YTI3NjQ2OGJlYWI2CmF1dGhvciB1c2VyMSA8YWRkcmVzczFAZXhhbXBsZS5jb20+IDE0ODk5NTY0NzkgLTA0MDAKY29tbWl0dGVyIEV0aGFuIEtvZW5pZyA8ZXRoYW50a29lbmlnQGdtYWlsLmNvbT4gMTQ4OTk1NjQ3OSAtMDQwMAoKSW5pdGlhbCBjb21taXQK"
	assert.Equal(t, expectedContent, *gitBlobResponse.Content)

	// Tests a private repo with no token so will fail
	req = NewRequestf(t, "GET", "/api/v1/repos/%s/%s/git/blobs/%s", user2.Name, repo16.Name, repo16ReadmeSHA)
	MakeRequest(t, req, http.StatusNotFound)

	// Test using access token for a private repo that the user of the token owns
	req = NewRequestf(t, "GET", "/api/v1/repos/%s/%s/git/blobs/%s", user2.Name, repo16.Name, repo16ReadmeSHA).
		AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	// Test using bad sha
	req = NewRequestf(t, "GET", "/api/v1/repos/%s/%s/git/blobs/%s", user2.Name, repo1.Name, badSHA)
	MakeRequest(t, req, http.StatusBadRequest)

	// Test using org repo "org3/repo3" where user2 is a collaborator
	req = NewRequestf(t, "GET", "/api/v1/repos/%s/%s/git/blobs/%s", org3.Name, repo3.Name, repo3ReadmeSHA).
		AddTokenAuth(token)
	MakeRequest(t, req, http.StatusOK)

	// Test using org repo "org3/repo3" with no user token
	req = NewRequestf(t, "GET", "/api/v1/repos/%s/%s/git/blobs/%s", org3.Name, repo3ReadmeSHA, repo3.Name)
	MakeRequest(t, req, http.StatusNotFound)

	// Login as User4.
	session = loginUser(t, user4.Name)
	token4 := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	// Test using org repo "org3/repo3" where user4 is a NOT collaborator
	req = NewRequestf(t, "GET", "/api/v1/repos/%s/%s/git/blobs/d56a3073c1dbb7b15963110a049d50cdb5db99fc?access=%s", org3.Name, repo3.Name, token4)
	MakeRequest(t, req, http.StatusNotFound)
}
