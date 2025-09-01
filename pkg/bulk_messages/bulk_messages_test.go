package bulk_messages

import (
	"testing"

	userMock "github.com/influenzanet/messaging-service/test/mocks/user-management-service"
	"github.com/influenzanet/user-management-service/pkg/api"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetTempLoginToken(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockUserClient := userMock.NewMockUserManagementApiClient(mockCtrl)

	// with error response
	t.Run("with error response", func(t *testing.T) {
		mockUserClient.EXPECT().GenerateTempToken(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, status.Error(codes.InvalidArgument, "missing argument"))

		_, err := getTemploginToken(
			mockUserClient,
			"testinstance",
			&api.User{Account: &api.User_Account{AccountId: "test"}},
			"teststudy",
			60,
		)
		if err == nil {
			t.Error("expected error")
		}
	})

	// with get token
	t.Run("with normal token response", func(t *testing.T) {
		mockUserClient.EXPECT().GenerateTempToken(
			gomock.Any(),
			gomock.Any(),
		).Return(&api.TempToken{Token: "testtoken"}, nil)

		token, err := getTemploginToken(
			mockUserClient,
			"testinstance",
			&api.User{Account: &api.User_Account{AccountId: "test"}},
			"teststudy",
			60,
		)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if token != "testtoken" {
			t.Errorf("unexpected token: %s", token)
		}
	})
}

func TestGetUnsubscribeToken(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockUserClient := userMock.NewMockUserManagementApiClient(mockCtrl)

	// with error response
	t.Run("with error response", func(t *testing.T) {
		mockUserClient.EXPECT().GetOrCreateTemptoken(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, status.Error(codes.InvalidArgument, "missing argument"))

		_, err := getUnsubscribeToken(mockUserClient, "testinstance", &api.User{Account: &api.User_Account{AccountId: "test"}})
		if err == nil {
			t.Error("expected error")
		}
	})

	// with get token
	t.Run("with normal token response", func(t *testing.T) {
		mockUserClient.EXPECT().GetOrCreateTemptoken(
			gomock.Any(),
			gomock.Any(),
		).Return(&api.TempToken{Token: "testtoken"}, nil)

		token, err := getUnsubscribeToken(mockUserClient, "testinstance", &api.User{Account: &api.User_Account{AccountId: "test"}})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if token != "testtoken" {
			t.Errorf("unexpected token: %s", token)
		}
	})
}
