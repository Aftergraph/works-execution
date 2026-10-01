package promotion

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

type promotionResolver struct{ value string; err error }
func (r promotionResolver) Resolve(context.Context,*secrets.Ref,string)(string,error){ return r.value,r.err }

func writePromotionJSON(w http.ResponseWriter, v any){
	w.Header().Set("Content-Type","application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newPromotionServiceForBackend(t *testing.T, b Backend) *Service {
	t.Helper()
	s,err:=NewService(&fakeEvidenceVerifier{},&fakeDecisionVerifier{},b)
	if err!=nil{t.Fatal(err)}
	return s
}

func rejectDangerousGitHubPath(t *testing.T, r *http.Request, targetBranch string){
	t.Helper()
	p:=r.URL.Path
	if strings.Contains(p,"/merge") ||
		strings.Contains(p,"/auto-merge") ||
		strings.Contains(p,"/reviews") ||
		strings.Contains(p,"/rulesets") ||
		strings.Contains(p,"/branches/") ||
		(r.Method==http.MethodPatch && strings.Contains(p,"/git/refs/heads/"+targetBranch)) {
		t.Fatalf("authority-expanding GitHub endpoint called: %s %s",r.Method,r.URL.String())
	}
}

func TestGitHubBackendCreatesDeterministicStagingPRWithoutTargetMutation(t *testing.T){
	req:=validRequest()
	var createdBranch,createdPR string
	ts:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		rejectDangerousGitHubPath(t,r,req.Target.Branch)
		if got:=r.Header.Get("Authorization"); got!="Bearer control-secret"{
			t.Fatalf("authorization=%q",got)
		}
		switch {
		case r.Method==http.MethodGet && strings.Contains(r.URL.Path,"/git/commits/"+req.Candidate.SHA):
			writePromotionJSON(w,map[string]any{"sha":req.Candidate.SHA})
		case r.Method==http.MethodGet && strings.Contains(r.URL.Path,"/git/matching-refs/heads/works/promotion/"):
			writePromotionJSON(w,[]any{})
		case r.Method==http.MethodPost && strings.HasSuffix(r.URL.Path,"/git/refs"):
			var body map[string]any; _=json.NewDecoder(r.Body).Decode(&body)
			createdBranch, _ = body["ref"].(string)
			if body["sha"]!=req.Candidate.SHA{t.Fatalf("staging sha=%v want %s",body["sha"],req.Candidate.SHA)}
			writePromotionJSON(w,map[string]any{"ref":createdBranch,"object":map[string]any{"sha":req.Candidate.SHA,"type":"commit"}})
		case r.Method==http.MethodGet && strings.HasSuffix(r.URL.Path,"/pulls"):
			if r.URL.Query().Get("state")!="open" || r.URL.Query().Get("base")!="main"{
				t.Fatalf("bad PR query: %s",r.URL.RawQuery)
			}
			writePromotionJSON(w,[]any{})
		case r.Method==http.MethodPost && strings.HasSuffix(r.URL.Path,"/pulls"):
			var body map[string]any; _=json.NewDecoder(r.Body).Decode(&body)
			createdPR,_=body["body"].(string)
			if body["base"]!="main"{t.Fatalf("PR base=%v",body["base"])}
			if !strings.Contains(createdPR,req.Candidate.SHA) ||
				!strings.Contains(createdPR,req.EvidenceBundleID) ||
				!strings.Contains(createdPR,req.DecisionRef) {
				t.Fatalf("PR body missing provenance: %s",createdPR)
			}
			writePromotionJSON(w,map[string]any{
				"number":17,"html_url":"https://github.com/Aftergraph/runtime/pull/17",
				"body":createdPR,
				"head":map[string]any{"ref":strings.TrimPrefix(createdBranch,"refs/heads/")},
				"base":map[string]any{"ref":"main"},
			})
		default:
			t.Fatalf("unexpected GitHub request: %s %s",r.Method,r.URL.String())
		}
	}))
	defer ts.Close()

	b,err:=NewGitHubBackend(GitHubConfig{
		ControlTokenRef:secrets.Must("secret://github/control"),
		APIBase:ts.URL,WebBase:"https://github.com",BranchPrefix:"works/promotion",
	},promotionResolver{value:"control-secret"},ts.Client())
	if err!=nil{t.Fatal(err)}

	p,err:=newPromotionServiceForBackend(t,b).Propose(context.Background(),req)
	if err!=nil{t.Fatal(err)}
	if p.PullRequestNumber!=17 || p.CandidateSHA!=req.Candidate.SHA{t.Fatalf("bad proposal: %#v",p)}
	if !strings.HasPrefix(createdBranch,"refs/heads/works/promotion/"){t.Fatalf("branch=%q",createdBranch)}
	if !strings.Contains(createdPR,"aftergraph-promotion:v1"){t.Fatalf("machine marker absent: %s",createdPR)}
}

func TestGitHubBackendExactReplayReusesStagingBranchAndPR(t *testing.T){
	req:=validRequest()
	key,fp,err:=promotionIdentity(req); if err!=nil{t.Fatal(err)}
	branch:="works/promotion/"+key+"-"+fp
	marker:=promotionMarker("prp_"+key+"-"+fp,req)

	var creates int
	ts:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		rejectDangerousGitHubPath(t,r,req.Target.Branch)
		switch {
		case r.Method==http.MethodGet && strings.Contains(r.URL.Path,"/git/commits/"+req.Candidate.SHA):
			writePromotionJSON(w,map[string]any{"sha":req.Candidate.SHA})
		case r.Method==http.MethodGet && strings.Contains(r.URL.Path,"/git/matching-refs/heads/works/promotion/"):
			writePromotionJSON(w,[]any{map[string]any{"ref":"refs/heads/"+branch,"object":map[string]any{"sha":req.Candidate.SHA,"type":"commit"}}})
		case r.Method==http.MethodGet && strings.HasSuffix(r.URL.Path,"/pulls"):
			writePromotionJSON(w,[]any{map[string]any{
				"number":17,"html_url":"https://github.com/Aftergraph/runtime/pull/17",
				"created_at":"2026-10-01T18:00:00Z",
				"body":marker,"head":map[string]any{"ref":branch},"base":map[string]any{"ref":"main"},
			}})
		case r.Method==http.MethodPost:
			creates++; t.Fatalf("replay attempted create: %s",r.URL.Path)
		default:
			t.Fatalf("unexpected: %s %s",r.Method,r.URL.String())
		}
	}))
	defer ts.Close()
	b,err:=NewGitHubBackend(GitHubConfig{
		ControlTokenRef:secrets.Must("secret://github/control"),APIBase:ts.URL,WebBase:"https://github.com",
		BranchPrefix:"works/promotion",
	},promotionResolver{value:"control-secret"},ts.Client()); if err!=nil{t.Fatal(err)}
	p,err:=newPromotionServiceForBackend(t,b).Propose(context.Background(),req); if err!=nil{t.Fatal(err)}
	again,err:=newPromotionServiceForBackend(t,b).Propose(context.Background(),req); if err!=nil{t.Fatal(err)}
	if creates!=0 || p.PullRequestNumber!=17{t.Fatalf("replay created resource or bad proposal: creates=%d %#v",creates,p)}
	if p != again {
		t.Fatalf("exact replay changed durable proposal: first=%#v again=%#v",p,again)
	}
	if p.CreatedAt.IsZero() {
		t.Fatal("proposal lost GitHub PR creation time")
	}
}

func TestGitHubBackendRejectsConflictingDeterministicRef(t *testing.T){
	req:=validRequest()
	key,_,err:=promotionIdentity(req); if err!=nil{t.Fatal(err)}
	ts:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		switch {
		case r.Method==http.MethodGet && strings.Contains(r.URL.Path,"/git/commits/"+req.Candidate.SHA):
			writePromotionJSON(w,map[string]any{"sha":req.Candidate.SHA})
		case r.Method==http.MethodGet && strings.Contains(r.URL.Path,"/git/matching-refs/heads/works/promotion/"):
			writePromotionJSON(w,[]any{map[string]any{
				"ref":"refs/heads/works/promotion/"+key+"-differentfingerprint",
				"object":map[string]any{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","type":"commit"},
			}})
		default:t.Fatalf("conflict reached later GitHub operation: %s %s",r.Method,r.URL.String())
		}
	}))
	defer ts.Close()
	b,_:=NewGitHubBackend(GitHubConfig{ControlTokenRef:secrets.Must("secret://github/control"),APIBase:ts.URL},promotionResolver{value:"control-secret"},ts.Client())
	_,err=newPromotionServiceForBackend(t,b).Propose(context.Background(),req)
	if !errors.Is(err,ErrIdempotencyConflict){t.Fatalf("got %v want ErrIdempotencyConflict",err)}
}

func TestGitHubBackendRejectsExistingPRWithWrongMarker(t *testing.T){
	req:=validRequest()
	key,fp,_:=promotionIdentity(req)
	branch:="works/promotion/"+key+"-"+fp
	ts:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		switch{
		case r.Method==http.MethodGet && strings.Contains(r.URL.Path,"/git/commits/"+req.Candidate.SHA):
			writePromotionJSON(w,map[string]any{"sha":req.Candidate.SHA})
		case r.Method==http.MethodGet && strings.Contains(r.URL.Path,"/git/matching-refs/heads/works/promotion/"):
			writePromotionJSON(w,[]any{map[string]any{"ref":"refs/heads/"+branch,"object":map[string]any{"sha":req.Candidate.SHA,"type":"commit"}}})
		case r.Method==http.MethodGet && strings.HasSuffix(r.URL.Path,"/pulls"):
			writePromotionJSON(w,[]any{map[string]any{"number":17,"html_url":"x","body":"foreign PR","head":map[string]any{"ref":branch},"base":map[string]any{"ref":"main"}}})
		default:t.Fatalf("unexpected: %s %s",r.Method,r.URL.String())
		}
	}))
	defer ts.Close()
	b,_:=NewGitHubBackend(GitHubConfig{ControlTokenRef:secrets.Must("secret://github/control"),APIBase:ts.URL},promotionResolver{value:"control-secret"},ts.Client())
	_,err:=newPromotionServiceForBackend(t,b).Propose(context.Background(),req)
	if !errors.Is(err,ErrIdempotencyConflict){t.Fatalf("got %v",err)}
}

func TestGitHubBackendPRQueryEncodesOwnerHead(t *testing.T){
	req:=validRequest()
	key,fp,_:=promotionIdentity(req); branch:="works/promotion/"+key+"-"+fp
	q:=url.Values{"state":{"open"},"head":{"Aftergraph:"+branch},"base":{"main"}}
	if !strings.Contains(q.Encode(),"head=Aftergraph%3Aworks%2Fpromotion%2F"){t.Fatalf("unexpected query encoding: %s",q.Encode())}
}


func TestGitHubBackendCandidateNotFoundFailsClosed(t *testing.T) {
	req := validRequest()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"+req.Candidate.SHA) {
			http.NotFound(w, r)
			return
		}
		t.Fatalf("candidate 404 reached later operation: %s %s", r.Method, r.URL.String())
	}))
	defer ts.Close()
	b, _ := NewGitHubBackend(GitHubConfig{
		ControlTokenRef: secrets.Must("secret://github/control"), APIBase: ts.URL,
	}, promotionResolver{value:"control-secret"}, ts.Client())
	_, err := newPromotionServiceForBackend(t,b).Propose(context.Background(), req)
	if !errors.Is(err, ErrImmutableCandidateRequired) {
		t.Fatalf("got %v want ErrImmutableCandidateRequired", err)
	}
}

func TestGitHubBackendProviderFailuresAreRetryable(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			req := validRequest()
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"+req.Candidate.SHA) {
					w.WriteHeader(status)
					return
				}
				t.Fatalf("provider failure reached later operation: %s %s", r.Method, r.URL.String())
			}))
			defer ts.Close()
			b, _ := NewGitHubBackend(GitHubConfig{
				ControlTokenRef: secrets.Must("secret://github/control"), APIBase: ts.URL,
			}, promotionResolver{value:"control-secret"}, ts.Client())
			_, err := newPromotionServiceForBackend(t,b).Propose(context.Background(), req)
			if !errors.Is(err, ErrProviderUnavailable) {
				t.Fatalf("status=%d got %v want ErrProviderUnavailable", status, err)
			}
		})
	}
}

func TestGitHubBackendDuplicatePRRaceRecovers(t *testing.T) {
	req := validRequest()
	key, fp, _ := promotionIdentity(req)
	branch := "works/promotion/"+key+"-"+fp
	id := "prp_"+key+"-"+fp
	marker := promotionMarker(id, req)
	pullReads := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rejectDangerousGitHubPath(t,r,req.Target.Branch)
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"+req.Candidate.SHA):
			writePromotionJSON(w,map[string]any{"sha":req.Candidate.SHA})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/matching-refs/heads/works/promotion/"):
			writePromotionJSON(w,[]any{map[string]any{
				"ref":"refs/heads/"+branch,
				"object":map[string]any{"sha":req.Candidate.SHA,"type":"commit"},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
			pullReads++
			if pullReads == 1 {
				writePromotionJSON(w,[]any{})
				return
			}
			writePromotionJSON(w,[]any{map[string]any{
				"number":23,"html_url":"https://github.com/Aftergraph/runtime/pull/23",
				"body":marker,"head":map[string]any{"ref":branch},"base":map[string]any{"ref":"main"},
			}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			writePromotionJSON(w,map[string]any{"message":"A pull request already exists"})
		default:
			t.Fatalf("unexpected: %s %s",r.Method,r.URL.String())
		}
	}))
	defer ts.Close()

	b,_:=NewGitHubBackend(GitHubConfig{
		ControlTokenRef:secrets.Must("secret://github/control"),APIBase:ts.URL,
	},promotionResolver{value:"control-secret"},ts.Client())
	p,err:=newPromotionServiceForBackend(t,b).Propose(context.Background(),req)
	if err!=nil { t.Fatal(err) }
	if p.PullRequestNumber!=23 || pullReads!=2 {
		t.Fatalf("race reconciliation failed: pulls=%d proposal=%#v",pullReads,p)
	}
}

func TestGitHubBackendDoesNotDeletePreexistingStagingRefOnPRFailure(t *testing.T) {
	req := validRequest()
	key, fp, _ := promotionIdentity(req)
	branch := "works/promotion/"+key+"-"+fp
	deletes := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes++
			t.Fatalf("pre-existing staging ref was deleted: %s",r.URL.Path)
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/commits/"+req.Candidate.SHA):
			writePromotionJSON(w,map[string]any{"sha":req.Candidate.SHA})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/matching-refs/heads/works/promotion/"):
			writePromotionJSON(w,[]any{map[string]any{
				"ref":"refs/heads/"+branch,
				"object":map[string]any{"sha":req.Candidate.SHA,"type":"commit"},
			}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
			writePromotionJSON(w,[]any{})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected: %s %s",r.Method,r.URL.String())
		}
	}))
	defer ts.Close()

	b,_:=NewGitHubBackend(GitHubConfig{
		ControlTokenRef:secrets.Must("secret://github/control"),APIBase:ts.URL,
	},promotionResolver{value:"control-secret"},ts.Client())
	_,err:=newPromotionServiceForBackend(t,b).Propose(context.Background(),req)
	if !errors.Is(err,ErrProviderUnavailable) {
		t.Fatalf("got %v want ErrProviderUnavailable",err)
	}
	if deletes!=0 { t.Fatalf("delete calls=%d",deletes) }
}
