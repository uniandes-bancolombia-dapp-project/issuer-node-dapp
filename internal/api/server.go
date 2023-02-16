package api

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	core "github.com/iden3/go-iden3-core"
	"github.com/iden3/go-schema-processor/verifiable"
	"github.com/iden3/iden3comm"
	"github.com/iden3/iden3comm/packers"

	"github.com/polygonid/sh-id-platform/internal/config"
	"github.com/polygonid/sh-id-platform/internal/core/ports"
	"github.com/polygonid/sh-id-platform/internal/core/services"
	"github.com/polygonid/sh-id-platform/internal/gateways"
	"github.com/polygonid/sh-id-platform/internal/health"
	"github.com/polygonid/sh-id-platform/internal/repositories"
)

// Server implements StrictServerInterface and holds the implementation of all API controllers
// This is the glue to the API autogenerated code
type Server struct {
	cfg              *config.Configuration
	identityService  ports.IdentityService
	claimService     ports.ClaimsService
	schemaService    ports.SchemaService
	publisherGateway ports.Publisher
	packageManager   *iden3comm.PackageManager
	health           *health.Status
}

// NewServer is a Server constructor
func NewServer(cfg *config.Configuration, identityService ports.IdentityService, claimsService ports.ClaimsService, schemaService ports.SchemaService, publisherGateway ports.Publisher, packageManager *iden3comm.PackageManager, health *health.Status) *Server {
	return &Server{
		cfg:              cfg,
		identityService:  identityService,
		claimService:     claimsService,
		schemaService:    schemaService,
		publisherGateway: publisherGateway,
		packageManager:   packageManager,
		health:           health,
	}
}

// Health is a method
func (s *Server) Health(_ context.Context, _ HealthRequestObject) (HealthResponseObject, error) {
	var resp Health200JSONResponse = s.health.Status()

	return resp, nil
}

// GetDocumentation this method will be overridden in the main function
func (s *Server) GetDocumentation(_ context.Context, _ GetDocumentationRequestObject) (GetDocumentationResponseObject, error) {
	return nil, nil
}

// GetYaml this method will be overridden in the main function
func (s *Server) GetYaml(_ context.Context, _ GetYamlRequestObject) (GetYamlResponseObject, error) {
	return nil, nil
}

// CreateIdentity is created identity controller
func (s *Server) CreateIdentity(ctx context.Context, request CreateIdentityRequestObject) (CreateIdentityResponseObject, error) {
	method := request.Body.DidMetadata.Method
	blockchain := request.Body.DidMetadata.Blockchain
	network := request.Body.DidMetadata.Network

	identity, err := s.identityService.Create(ctx, method, blockchain, network, s.cfg.ServerUrl)
	if err != nil {
		if errors.Is(err, services.ErrWrongDIDMetada) {
			return CreateIdentity400JSONResponse{
				N400JSONResponse{
					Message: err.Error(),
				},
			}, nil
		}
		return nil, err
	}

	return CreateIdentity201JSONResponse{
		Identifier: &identity.Identifier,
		State: &IdentityState{
			BlockNumber:        identity.State.BlockNumber,
			BlockTimestamp:     identity.State.BlockTimestamp,
			ClaimsTreeRoot:     identity.State.ClaimsTreeRoot,
			CreatedAt:          identity.State.CreatedAt,
			ModifiedAt:         identity.State.ModifiedAt,
			PreviousState:      identity.State.PreviousState,
			RevocationTreeRoot: identity.State.RevocationTreeRoot,
			RootOfRoots:        identity.State.RootOfRoots,
			State:              identity.State.State,
			Status:             string(identity.State.Status),
			TxID:               identity.State.TxID,
		},
	}, nil
}

// CreateClaim is claim creation controller
func (s *Server) CreateClaim(ctx context.Context, request CreateClaimRequestObject) (CreateClaimResponseObject, error) {
	did, err := core.ParseDID(request.Identifier)
	if err != nil {
		return CreateClaim400JSONResponse{N400JSONResponse{Message: err.Error()}}, nil
	}

	req := ports.NewCreateClaimRequest(did, request.Body.CredentialSchema, request.Body.CredentialSubject, request.Body.Expiration, request.Body.Type, request.Body.Version, request.Body.SubjectPosition, request.Body.MerklizedRootPosition)

	resp, err := s.claimService.CreateClaim(ctx, req)
	if err != nil {
		if errors.Is(err, services.ErrJSONLdContext) {
			return CreateClaim400JSONResponse{N400JSONResponse{Message: err.Error()}}, nil
		}
		if errors.Is(err, services.ErrProcessSchema) {
			return CreateClaim400JSONResponse{N400JSONResponse{Message: err.Error()}}, nil
		}
		if errors.Is(err, services.ErrLoadingSchema) {
			return CreateClaim422JSONResponse{N422JSONResponse{Message: err.Error()}}, nil
		}
		if errors.Is(err, services.ErrMalformedURL) {
			return CreateClaim400JSONResponse{N400JSONResponse{Message: err.Error()}}, nil
		}
		return CreateClaim500JSONResponse{N500JSONResponse{Message: err.Error()}}, nil
	}
	return CreateClaim201JSONResponse{Id: resp.ID.String()}, nil
}

// RevokeClaim is the revocation claim controller
func (s *Server) RevokeClaim(ctx context.Context, request RevokeClaimRequestObject) (RevokeClaimResponseObject, error) {
	if err := s.claimService.Revoke(ctx, request.Identifier, uint64(request.Nonce), ""); err != nil {
		if errors.Is(err, repositories.ErrClaimDoesNotExist) {
			return RevokeClaim404JSONResponse{N404JSONResponse{
				Message: "the claim does not exist",
			}}, nil
		}

		return RevokeClaim500JSONResponse{N500JSONResponse{Message: err.Error()}}, nil
	}
	return RevokeClaim202JSONResponse{
		Message: "claim revocation request sent",
	}, nil
}

// GetRevocationStatus is the controller to get revocation status
func (s *Server) GetRevocationStatus(ctx context.Context, request GetRevocationStatusRequestObject) (GetRevocationStatusResponseObject, error) {
	response := GetRevocationStatus200JSONResponse{}
	var err error

	rs, err := s.claimService.GetRevocationStatus(ctx, request.Identifier, uint64(request.Nonce))
	if err != nil {
		return GetRevocationStatus500JSONResponse{N500JSONResponse{
			Message: err.Error(),
		}}, nil
	}

	response.Issuer.State = rs.Issuer.State
	response.Issuer.RevocationTreeRoot = rs.Issuer.RevocationTreeRoot
	response.Issuer.RootOfRoots = rs.Issuer.RootOfRoots
	response.Issuer.ClaimsTreeRoot = rs.Issuer.ClaimsTreeRoot
	response.Mtp.Existence = rs.MTP.Existence

	if rs.MTP.NodeAux != nil {
		key, _ := rs.MTP.NodeAux.Key.MarshalText()
		value, _ := rs.MTP.NodeAux.Value.MarshalText()
		response.Mtp.NodeAux = &struct {
			Key   *ByteArray `json:"key,omitempty"`
			Value *ByteArray `json:"value,omitempty"`
		}{
			Key:   &key,
			Value: &value,
		}
	}
	response.Mtp.Existence = rs.MTP.Existence
	siblings := make([]ByteArray, 0)
	for _, s := range rs.MTP.AllSiblings() {
		sb, _ := s.MarshalText()
		siblings = append(siblings, sb)
	}
	response.Mtp.Siblings = &siblings
	return response, err
}

// GetClaim is the controller to get a client.
func (s *Server) GetClaim(ctx context.Context, request GetClaimRequestObject) (GetClaimResponseObject, error) {
	if request.Identifier == "" {
		return GetClaim400JSONResponse{N400JSONResponse{"invalid did, can not be empty"}}, nil
	}

	did, err := core.ParseDID(request.Identifier)
	if err != nil {
		return GetClaim400JSONResponse{N400JSONResponse{"invalid did"}}, nil
	}

	if request.Id == "" {
		return GetClaim400JSONResponse{N400JSONResponse{"can not proceed with an empty claim id"}}, nil
	}

	clID, err := uuid.Parse(request.Id)
	if err != nil {
		return GetClaim400JSONResponse{N400JSONResponse{"invalid claim id"}}, nil
	}

	claim, err := s.claimService.GetByID(ctx, did, clID)
	if err != nil {
		if errors.Is(err, services.ErrClaimNotFound) {
			return GetClaim404JSONResponse{N404JSONResponse{err.Error()}}, nil
		}
		return GetClaim500JSONResponse{N500JSONResponse{err.Error()}}, nil
	}

	w3c, err := s.schemaService.FromClaimModelToW3CCredential(*claim)
	if err != nil {
		return GetClaim500JSONResponse{N500JSONResponse{"invalid claim format"}}, nil
	}

	return GetClaim200JSONResponse(toGetClaim200Response(w3c)), nil
}

// GetClaims is the controller to get multiple claims of a determined identity
func (s *Server) GetClaims(ctx context.Context, request GetClaimsRequestObject) (GetClaimsResponseObject, error) {
	if request.Identifier == "" {
		return GetClaims400JSONResponse{N400JSONResponse{"invalid did, can not be empty"}}, nil
	}

	did, err := core.ParseDID(request.Identifier)
	if err != nil {
		return GetClaims400JSONResponse{N400JSONResponse{"invalid did"}}, nil
	}

	filter, err := ports.NewClaimsFilter(request.Params.SchemaHash, request.Params.SchemaType, request.Params.Subject, request.Params.QueryField, request.Params.Self, request.Params.Revoked)
	if err != nil {
		return GetClaims400JSONResponse{N400JSONResponse{err.Error()}}, nil
	}

	claims, err := s.claimService.GetAll(ctx, did, filter)
	if err != nil {
		return GetClaims500JSONResponse{N500JSONResponse{"there was an internal error trying to retrieve claims for the requested identifier"}}, nil
	}

	return toGetClaims200Response(claims), nil
}

// GetIdentities is the controller to get identities
func (s *Server) GetIdentities(ctx context.Context, request GetIdentitiesRequestObject) (GetIdentitiesResponseObject, error) {
	var response GetIdentities200JSONResponse
	var err error
	response, err = s.identityService.Get(ctx)
	if err != nil {
		return GetIdentities500JSONResponse{N500JSONResponse{
			Message: err.Error(),
		}}, nil
	}

	return response, nil
}

// Agent is the controller to fetch credentials from mobile
func (s *Server) Agent(ctx context.Context, request AgentRequestObject) (AgentResponseObject, error) {
	if request.Body == nil || *request.Body == "" {
		return Agent400JSONResponse{N400JSONResponse{"can not proceed with an empty request"}}, nil
	}
	basicMessage, err := s.packageManager.UnpackWithType(packers.MediaTypeZKPMessage, []byte(*request.Body))
	if err != nil {
		return Agent400JSONResponse{N400JSONResponse{"can not proceed with the given request"}}, nil
	}

	req, err := ports.NewAgentRequest(basicMessage)
	if err != nil {
		return Agent400JSONResponse{N400JSONResponse{err.Error()}}, nil
	}

	agent, err := s.claimService.Agent(ctx, req)
	if err != nil {
		return Agent400JSONResponse{N400JSONResponse{err.Error()}}, nil
	}

	return Agent200JSONResponse{
		Body:     agent.Body,
		From:     agent.From,
		Id:       agent.ID,
		ThreadID: agent.ThreadID,
		To:       agent.To,
		Typ:      string(agent.Typ),
		Type:     string(agent.Type),
	}, nil
}

// PublishIdentityState - publish identity state on chain
func (s *Server) PublishIdentityState(ctx context.Context, request PublishIdentityStateRequestObject) (PublishIdentityStateResponseObject, error) {
	did, err := core.ParseDID(request.Identifier)
	if err != nil {
		return PublishIdentityState400JSONResponse{N400JSONResponse{"invalid did"}}, nil
	}

	publishedState, err := s.publisherGateway.PublishState(ctx, did)
	if err != nil {
		if errors.Is(err, gateways.ErrNoStatesToProcess) || errors.Is(err, gateways.ErrStateIsBeingProcessed) {
			return PublishIdentityState200JSONResponse{Message: err.Error()}, nil
		}
		return PublishIdentityState500JSONResponse{N500JSONResponse{err.Error()}}, nil
	}

	return PublishIdentityState202JSONResponse{
		ClaimsTreeRoot:     publishedState.ClaimsTreeRoot,
		RevocationTreeRoot: publishedState.RevocationTreeRoot,
		RootOfRoots:        publishedState.RootOfRoots,
		State:              publishedState.State,
		TxID:               publishedState.TxID,
	}, nil
}

// RegisterStatic add method to the mux that are not documented in the API.
func RegisterStatic(mux *chi.Mux) {
	mux.Get("/", documentation)
	mux.Get("/static/docs/api/api.yaml", swagger)
}

func toGetClaims200Response(claims []*verifiable.W3CCredential) GetClaims200JSONResponse {
	response := make(GetClaims200JSONResponse, len(claims))
	for i := range claims {
		response[i] = toGetClaim200Response(claims[i])
	}

	return response
}

func toGetClaim200Response(claim *verifiable.W3CCredential) GetClaimResponse {
	return GetClaimResponse{
		Context: claim.Context,
		CredentialSchema: CredentialSchema{
			claim.CredentialSchema.ID,
			claim.CredentialSchema.Type,
		},
		CredentialStatus:  claim.CredentialStatus,
		CredentialSubject: claim.CredentialSubject,
		Expiration:        claim.Expiration,
		Id:                claim.ID,
		IssuanceDate:      claim.IssuanceDate,
		Issuer:            claim.Issuer,
		Proof:             claim.Proof,
		Type:              claim.Type,
	}
}

func documentation(w http.ResponseWriter, _ *http.Request) {
	writeFile("api/spec.html", w)
}

func swagger(w http.ResponseWriter, _ *http.Request) {
	writeFile("api/api.yaml", w)
}

func writeFile(path string, w http.ResponseWriter) {
	f, err := os.ReadFile(path)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(f)
}
