package promotion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/JonasAbde/works-execution/packages/secrets"
)

const GitHubBackendID = "github"

type GitHubConfig struct {
	ControlTokenRef *secrets.Ref
	ControlScope    string
	APIBase         string
	WebBase         string
	BranchPrefix    string
}

type GitHubBackend struct {
	cfg      GitHubConfig
	resolver secrets.Resolver
	client   *http.Client
}

func NewGitHubBackend(cfg GitHubConfig, resolver secrets.Resolver, client *http.Client) (*GitHubBackend, error) {
	if cfg.ControlTokenRef == nil || resolver == nil {
		return nil, fmt.Errorf("%w: github control token ref and resolver are required", ErrMalformed)
	}
	if cfg.APIBase == "" { cfg.APIBase = "https://api.github.com" }
	if cfg.WebBase == "" { cfg.WebBase = "https://github.com" }
	if cfg.BranchPrefix == "" { cfg.BranchPrefix = "works/promotion" }
	cfg.BranchPrefix = strings.Trim(cfg.BranchPrefix, "/")
	if cfg.BranchPrefix == "" {
		return nil, fmt.Errorf("%w: github branch prefix is empty", ErrMalformed)
	}
	if client == nil { client = http.DefaultClient }
	return &GitHubBackend{cfg:cfg,resolver:resolver,client:client},nil
}

func (b *GitHubBackend) ID() string { return GitHubBackendID }

type promotionGitRef struct {
	Ref string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

type promotionPull struct {
	Number int `json:"number"`
	HTMLURL string `json:"html_url"`
	Body string `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Head struct { Ref string `json:"ref"` } `json:"head"`
	Base struct { Ref string `json:"ref"` } `json:"base"`
}

var promotionRepoRE=regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (b *GitHubBackend) Propose(ctx context.Context, auth AuthorizedRequest) (Proposal,error) {
	req:=auth.Request()
	if req.Target.Provider!="github" {
		return Proposal{},ErrUnsupportedTarget
	}
	if !promotionRepoRE.MatchString(req.Target.Repository) || req.Candidate.Repository!=req.Target.Repository {
		return Proposal{},fmt.Errorf("%w: v1 requires candidate in target GitHub repository",ErrUnsupportedTarget)
	}
	token,err:=b.controlToken(ctx)
	if err!=nil{return Proposal{},err}
	if err:=b.verifyCommit(ctx,token,req.Target.Repository,req.Candidate.SHA);err!=nil{return Proposal{},err}

	branch:=b.cfg.BranchPrefix+"/"+auth.KeyHash()+"-"+auth.Fingerprint()
	keyPrefix:=b.cfg.BranchPrefix+"/"+auth.KeyHash()+"-"
	if err:=b.ensureStagingRef(ctx,token,req.Target.Repository,branch,keyPrefix,req.Candidate.SHA);err!=nil{
		return Proposal{},err
	}
	id:="prp_"+auth.KeyHash()+"-"+auth.Fingerprint()
	marker:=promotionMarker(id,req)
	body:=promotionPRBody(id,req,marker)

	pr,err:=b.ensurePullRequest(ctx,token,req.Target.Repository,branch,req.Target.Branch,id,marker,body)
	if err!=nil{return Proposal{},err}
	return proposalFromPull(id,req,branch,pr),nil
}

func (b *GitHubBackend) Get(ctx context.Context, proposal Proposal)(Proposal,error){
	if proposal.Target.Provider!="github" || !promotionRepoRE.MatchString(proposal.Target.Repository) || proposal.PullRequestNumber<1 {
		return Proposal{},ErrMalformed
	}
	token,err:=b.controlToken(ctx); if err!=nil{return Proposal{},err}
	var pr promotionPull
	if err:=b.doJSON(ctx,token,http.MethodGet,fmt.Sprintf("/repos/%s/pulls/%d",proposal.Target.Repository,proposal.PullRequestNumber),nil,&pr);err!=nil{
		return Proposal{},err
	}
	if pr.Number!=proposal.PullRequestNumber || pr.Head.Ref!=strings.TrimPrefix(proposal.StagingRef,"refs/heads/") || pr.Base.Ref!=proposal.Target.Branch {
		return Proposal{},ErrIdempotencyConflict
	}
	if !strings.Contains(pr.Body, proposalMarker(proposal)) {
		return Proposal{},ErrIdempotencyConflict
	}
	var ref promotionGitRef
	refPath := strings.TrimPrefix(proposal.StagingRef, "refs/heads/")
	if err:=b.doJSON(ctx,token,http.MethodGet,"/repos/"+proposal.Target.Repository+"/git/ref/heads/"+promotionRefPath(refPath),nil,&ref);err!=nil{
		return Proposal{},err
	}
	if ref.Ref!=proposal.StagingRef || ref.Object.SHA!=proposal.CandidateSHA {
		return Proposal{},ErrIdempotencyConflict
	}
	return proposal,nil
}

func (b *GitHubBackend) verifyCommit(ctx context.Context,token,repository,sha string)error{
	var out struct{SHA string `json:"sha"`}
	if err:=b.doJSON(ctx,token,http.MethodGet,"/repos/"+repository+"/git/commits/"+url.PathEscape(sha),nil,&out);err!=nil{
		if errors.Is(err,ErrNotFound){return ErrImmutableCandidateRequired}
		return err
	}
	if out.SHA!=sha{return ErrImmutableCandidateRequired}
	return nil
}

func (b *GitHubBackend) ensureStagingRef(ctx context.Context,token,repository,branch,keyPrefix,sha string)error{
	refs,err:=b.matchingRefs(ctx,token,repository,keyPrefix)
	if err!=nil{return err}
	exact,conflict:=classifyPromotionRefs(refs,branch,keyPrefix,sha)
	if conflict{return ErrIdempotencyConflict}
	if exact{return nil}

	var out promotionGitRef
	err=b.doJSON(ctx,token,http.MethodPost,"/repos/"+repository+"/git/refs",map[string]any{
		"ref":"refs/heads/"+branch,"sha":sha,
	},&out)
	if err==nil{return nil}
	if !errors.Is(err,ErrProviderUnavailable){return err}

	refs,readErr:=b.matchingRefs(ctx,token,repository,keyPrefix)
	if readErr!=nil{return err}
	exact,conflict=classifyPromotionRefs(refs,branch,keyPrefix,sha)
	if conflict{return ErrIdempotencyConflict}
	if exact{return nil}
	return err
}

func (b *GitHubBackend) matchingRefs(ctx context.Context,token,repository,prefix string)([]promotionGitRef,error){
	var out []promotionGitRef
	path:="/repos/"+repository+"/git/matching-refs/heads/"+promotionRefPath(prefix)
	if err:=b.doJSON(ctx,token,http.MethodGet,path,nil,&out);err!=nil{return nil,err}
	return out,nil
}

func classifyPromotionRefs(refs []promotionGitRef,exactBranch,keyPrefix,sha string)(bool,bool){
	conflict:=false
	for _,ref:=range refs{
		br:=strings.TrimPrefix(ref.Ref,"refs/heads/")
		if br==exactBranch {
			if ref.Object.SHA==sha{return true,false}
			return false,true
		}
		if strings.HasPrefix(br,keyPrefix){conflict=true}
	}
	return false,conflict
}

func (b *GitHubBackend) ensurePullRequest(ctx context.Context,token,repository,branch,base,id,marker,body string)(promotionPull,error){
	prs,err:=b.listPulls(ctx,token,repository,branch,base)
	if err!=nil{return promotionPull{},err}
	if pr,found,conflict:=classifyPromotionPulls(prs,branch,base,marker);conflict{
		return promotionPull{},ErrIdempotencyConflict
	}else if found{return pr,nil}

	var out promotionPull
	err=b.doJSON(ctx,token,http.MethodPost,"/repos/"+repository+"/pulls",map[string]any{
		"title":"WORKS source promotion "+id,
		"head":branch,
		"base":base,
		"body":body,
	},&out)
	if err==nil{return out,nil}
	if !errors.Is(err,ErrProviderUnavailable){return promotionPull{},err}

	prs,readErr:=b.listPulls(ctx,token,repository,branch,base)
	if readErr!=nil{return promotionPull{},err}
	if pr,found,conflict:=classifyPromotionPulls(prs,branch,base,marker);conflict{
		return promotionPull{},ErrIdempotencyConflict
	}else if found{return pr,nil}
	return promotionPull{},err
}

func (b *GitHubBackend) listPulls(ctx context.Context,token,repository,branch,base string)([]promotionPull,error){
	owner:=strings.SplitN(repository,"/",2)[0]
	q:=url.Values{}
	q.Set("state","all")
	q.Set("head",owner+":"+branch)
	q.Set("base",base)
	var out []promotionPull
	if err:=b.doJSON(ctx,token,http.MethodGet,"/repos/"+repository+"/pulls?"+q.Encode(),nil,&out);err!=nil{return nil,err}
	return out,nil
}

func classifyPromotionPulls(prs []promotionPull,branch,base,marker string)(promotionPull,bool,bool){
	for _,pr:=range prs{
		if pr.Head.Ref!=branch || pr.Base.Ref!=base {continue}
		if !strings.Contains(pr.Body,marker){return promotionPull{},false,true}
		return pr,true,false
	}
	return promotionPull{},false,false
}

func proposalMarker(p Proposal) string {
	return "<!-- aftergraph-promotion:v1\n"+
		"id="+p.ID+"\n"+
		"candidate="+p.CandidateSHA+"\n"+
		"evidence="+p.EvidenceBundleID+"\n"+
		"decision="+p.DecisionRef+"\n-->"
}

func promotionMarker(id string,req Request)string{
	return "<!-- aftergraph-promotion:v1\n"+
		"id="+id+"\n"+
		"candidate="+req.Candidate.SHA+"\n"+
		"evidence="+req.EvidenceBundleID+"\n"+
		"decision="+req.DecisionRef+"\n-->"
}

func promotionPRBody(id string,req Request,marker string)string{
	policy:=req.PolicyDecisionID
	if policy==""{policy="(none)"}
	return "Aftergraph WORKS source-promotion proposal\n\n"+
		"Work: "+req.WorkID+"\n"+
		"Candidate: "+req.Candidate.SHA+"\n"+
		"Evidence: "+req.EvidenceBundleID+"\n"+
		"Decision: "+req.DecisionRef+"\n"+
		"Policy decision: "+policy+"\n"+
		"Target: "+req.Target.Repository+"#"+req.Target.Branch+"\n\n"+
		"This PR represents a governed source proposal. Merge authority and release authority are separate.\n\n"+
		marker
}

func proposalFromPull(id string,req Request,branch string,pr promotionPull)Proposal{
	return Proposal{
		ID:id,Org:req.Org,WorkID:req.WorkID,CandidateSHA:req.Candidate.SHA,Target:req.Target,
		StagingRef:"refs/heads/"+branch,PullRequestURL:pr.HTMLURL,PullRequestNumber:pr.Number,
		EvidenceBundleID:req.EvidenceBundleID,DecisionRef:req.DecisionRef,PolicyDecisionID:req.PolicyDecisionID,
		CreatedAt:pr.CreatedAt.UTC(),
	}
}

func (b *GitHubBackend) controlToken(ctx context.Context)(string,error){
	v,err:=b.resolver.Resolve(ctx,b.cfg.ControlTokenRef,b.cfg.ControlScope)
	if err!=nil{return "",fmt.Errorf("%w: resolve github control token",ErrProviderUnavailable)}
	if v==""{return "",fmt.Errorf("%w: empty github control token",ErrProviderUnavailable)}
	return v,nil
}

func promotionRefPath(ref string)string{
	parts:=strings.Split(ref,"/")
	for i:=range parts{parts[i]=url.PathEscape(parts[i])}
	return strings.Join(parts,"/")
}

func (b *GitHubBackend) doJSON(ctx context.Context,bearer,method,path string,body,out any)error{
	var reader io.Reader
	if body!=nil{
		raw,err:=json.Marshal(body);if err!=nil{return err}
		reader=bytes.NewReader(raw)
	}
	req,err:=http.NewRequestWithContext(ctx,method,strings.TrimRight(b.cfg.APIBase,"/")+path,reader)
	if err!=nil{return err}
	req.Header.Set("Authorization","Bearer "+bearer)
	req.Header.Set("Accept","application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version","2022-11-28")
	if body!=nil{req.Header.Set("Content-Type","application/json")}
	resp,err:=b.client.Do(req)
	if err!=nil{return fmt.Errorf("%w: github request failed",ErrProviderUnavailable)}
	defer resp.Body.Close()
	data,err:=io.ReadAll(io.LimitReader(resp.Body,2<<20));if err!=nil{return err}
	switch resp.StatusCode{
	case http.StatusNotFound:return ErrNotFound
	case http.StatusConflict,http.StatusUnprocessableEntity,http.StatusTooManyRequests:
		return fmt.Errorf("%w: github status %d",ErrProviderUnavailable,resp.StatusCode)
	}
	if resp.StatusCode>=500{return fmt.Errorf("%w: github status %d",ErrProviderUnavailable,resp.StatusCode)}
	if resp.StatusCode<200||resp.StatusCode>=300{return fmt.Errorf("%w: github status %d",ErrMalformed,resp.StatusCode)}
	if out==nil||len(data)==0{return nil}
	if err:=json.Unmarshal(data,out);err!=nil{return fmt.Errorf("%w: decode github response",ErrMalformed)}
	return nil
}

var _ Backend = (*GitHubBackend)(nil)
