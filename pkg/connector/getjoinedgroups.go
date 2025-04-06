package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// IGroupInfo represents a WhatsApp group with its properties
type IGroupInfo struct {
	ID                            string
	Name                          string
	Topic                         string
	IsParent                      bool
	OnlyAdminsCanSendMessages     bool
	MemberAddMode                 string
	DefaultMembershipApprovalMode string
	Participants                  []IGroupParticipant
}

// IGroupParticipant represents a member in a WhatsApp group
type IGroupParticipant struct {
	ID           string
	Name         string
	IsAdmin      bool
	IsSuperAdmin bool
	DisplayName  string
	Error        int
	AddRequest   *types.GroupParticipantAddRequest
}

// func getIntent(ctx context.Context) bridgev2.MatrixAPI {
// 	return ctx.Value(contextKeyIntent).(bridgev2.MatrixAPI)
// }

// func getPortal(ctx context.Context) *bridgev2.Portal {
// 	return ctx.Value(contextKeyPortal).(*bridgev2.Portal)
// }

// This is a method that calls the whatsmeow client to get the joined groups.
// It returns a slice of IGroupInfo, which is an interface that represents a WhatsApp group
// Not used at this time.
// func GetJoinedGroups(ctx context.Context) ([]IGroupInfo, error) {
// 	client := getClient(ctx)

// 	joinedGroups, err := client.GetJoinedGroups()
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to get joined groups: %w", err)
// 	}

// 	// Convert whatsmeow types to our interface types
// 	groups := make([]IGroupInfo, len(joinedGroups))
// 	for i, g := range joinedGroups {
// 		defaultApprovalMode := ""
// 		if g.GroupParent.IsParent {
// 			defaultApprovalMode = g.GroupParent.DefaultMembershipApprovalMode
// 		}

// 		groups[i] = IGroupInfo{
// 			ID:                            g.JID.String(),
// 			Name:                          g.Name,
// 			Topic:                         g.Topic,
// 			IsParent:                      g.IsParent,
// 			MemberAddMode:                 string(g.MemberAddMode),
// 			DefaultMembershipApprovalMode: defaultApprovalMode,
// 			OnlyAdminsCanSendMessages:     g.GroupAnnounce.IsAnnounce,
// 		}

// 		// Add participants with more details
// 		groups[i].Participants = make([]IGroupParticipant, len(g.Participants))
// 		for j, p := range g.Participants {
// 			groups[i].Participants[j] = IGroupParticipant{
// 				ID:           p.JID.String(),
// 				Name:         p.JID.User,
// 				IsAdmin:      p.IsAdmin,
// 				IsSuperAdmin: p.IsSuperAdmin,
// 				DisplayName:  p.DisplayName,
// 				Error:        p.Error,
// 				AddRequest:   p.AddRequest,
// 			}
// 		}
// 	}

// 	return groups, nil
// }

// GetFormattedGroups returns a JSON string with all WhatsApp groups the user is a member of
// Not used at this time.
// func GetFormattedGroups(ctx context.Context) (string, error) {
// 	client := getClient(ctx)

// 	// Get list of joined groups from whatsmeow
// 	groups, err := client.GetJoinedGroups()
// 	if err != nil {
// 		return "", fmt.Errorf("failed to get joined groups: %w", err)
// 	}

// 	// Handle empty groups case
// 	if len(groups) == 0 {
// 		wrapper := map[string]interface{}{
// 			"schema": "basic",
// 			// "user_phone_number": client.JID.User,
// 			"data": []map[string]interface{}{},
// 		}
// 		jsonData, err := json.Marshal(wrapper)
// 		if err != nil {
// 			return "", fmt.Errorf("failed to marshal groups to JSON: %w", err)
// 		}
// 		return string(jsonData), nil
// 	}

// 	// Filter and create a slice of group data
// 	var jsonGroups []map[string]interface{}
// 	for _, group := range groups {
// 		// Skip groups with less than 3 members and not parent groups
// 		if len(group.Participants) < 3 && !group.IsParent {
// 			continue
// 		}

// 		// For parent groups, check if the user is a participant
// 		if group.IsParent {
// 			isUserParticipant := false
// 			for _, p := range group.Participants {
// 				if p.JID.User == userWANumber {
// 					isUserParticipant = true
// 					break
// 				}
// 			}
// 			if !isUserParticipant {
// 				continue
// 			}
// 		}

// 		// Create basic group data
// 		groupData := map[string]interface{}{
// 			"jid":              group.JID.String(),
// 			"name":             group.Name,
// 			"participantCount": len(group.Participants),
// 		}

// 		// Include additional group details for better context
// 		groupData["memberAddMode"] = string(group.MemberAddMode)
// 		groupData["joinApprovalRequired"] = group.GroupMembershipApprovalMode.IsJoinApprovalRequired

// 		// Include parent group info if applicable
// 		if group.GroupParent.IsParent {
// 			groupData["isParentGroup"] = true
// 			groupData["defaultMembershipApprovalMode"] = group.GroupParent.DefaultMembershipApprovalMode
// 		}

// 		jsonGroups = append(jsonGroups, groupData)
// 	}

// 	// Create wrapper with schema, userWANumber, and data
// 	wrapper := map[string]interface{}{
// 		"schema":       "basic",
// 		"userWANumber": userWANumber,
// 		"data":         jsonGroups,
// 	}

// 	// Marshal to JSON
// 	jsonData, err := json.Marshal(wrapper)
// 	if err != nil {
// 		return "", fmt.Errorf("failed to marshal groups to JSON: %w", err)
// 	}

// 	return string(jsonData), nil
// }

// SendGroupsToReMatchBackend sends the WhatsApp groups to the ReMatch backend
func SendGroupsToReMatchBackend(ctx context.Context, wa *whatsmeow.Client, userJID types.JID) error {

	// Get list of joined groups from whatsmeow
	joinedGroups, err := wa.GetJoinedGroups()
	if err != nil {
		return fmt.Errorf("failed to get joined groups: %w", err)
	}

	userWANumber := userJID.User

	// Filter groups according to requirements
	var filteredGroups []*types.GroupInfo
	for _, group := range joinedGroups {
		// Skip groups with less than 3 members and not parent groups
		if len(group.Participants) < 3 && !group.IsParent {
			continue
		}

		// For parent groups, check if the user is a participant
		if group.IsParent {
			isUserParticipant := false
			for _, p := range group.Participants {
				if p.JID.User == userWANumber {
					isUserParticipant = true
					break
				}
			}
			if !isUserParticipant {
				continue
			}
		}

		filteredGroups = append(filteredGroups, group)
	}

	// Get the formatted JSON data for basic schema
	formattedGroups := make([]map[string]interface{}, len(filteredGroups))
	for i, group := range filteredGroups {
		// Create a basic group object with minimal information
		participantCount := len(group.Participants)
		formattedGroups[i] = map[string]interface{}{
			"jid":                           group.JID.String(),
			"name":                          group.Name,
			"participantCount":              participantCount, // backwards compatibility
			"participant_count":             participantCount,
			"only_admins_can_send_messages": group.GroupAnnounce.IsAnnounce,
		}
	}

	// Create detailed original data for raw schema
	originalGroups := make([]map[string]interface{}, len(filteredGroups))
	for i, group := range filteredGroups {
		// Process participants
		participants := make([]map[string]interface{}, len(group.Participants))
		for j, p := range group.Participants {
			// Convert error code to string if present
			errorStr := ""
			if p.Error != 0 {
				errorStr = fmt.Sprintf("%d", p.Error)
			}

			// Check if AddRequest exists
			hasAddRequest := p.AddRequest != nil

			participants[j] = map[string]interface{}{
				"jid":          p.JID.String(),
				"name":         p.JID.User,
				"isAdmin":      p.IsAdmin,
				"isSuperAdmin": p.IsSuperAdmin,
				"displayName":  p.DisplayName,
				"error":        errorStr,
				"addRequest":   hasAddRequest,
			}
		}

		// Create group data with all available information
		groupData := map[string]interface{}{
			"jid":                  group.JID.String(),
			"name":                 group.Name,
			"topic":                group.Topic,
			"isParent":             group.IsParent,
			"participants":         participants,
			"memberAddMode":        string(group.MemberAddMode),
			"created":              group.GroupCreated.Format(time.RFC3339),
			"joinApprovalRequired": group.GroupMembershipApprovalMode.IsJoinApprovalRequired,
		}

		// Include parent information if available
		if group.GroupParent.IsParent {
			groupData["isParentGroup"] = true
			groupData["defaultMembershipApprovalMode"] = group.GroupParent.DefaultMembershipApprovalMode
		}

		// Include linked parent JID if available
		if !group.GroupLinkedParent.LinkedParentJID.IsEmpty() {
			groupData["linkedParentJID"] = group.GroupLinkedParent.LinkedParentJID.String()
		}

		// Include owner JID if available
		if !group.OwnerJID.IsEmpty() {
			groupData["ownerJID"] = group.OwnerJID.String()
		}

		originalGroups[i] = groupData
	}

	// Create schema wrappers with userWANumber at the top level
	basicSchema := map[string]interface{}{
		"schema":       "basic",
		"userWANumber": userWANumber,
		"data":         formattedGroups,
	}

	rawSchema := map[string]interface{}{
		"schema":       "raw",
		"userWANumber": userWANumber,
		"data":         originalGroups,
	}

	// Marshal to JSON
	wrappedFormattedJSON, err := json.Marshal(basicSchema)
	if err != nil {
		return fmt.Errorf("failed to marshal basic schema: %w", err)
	}

	wrappedOriginalJSON, err := json.Marshal(rawSchema)
	if err != nil {
		return fmt.Errorf("failed to marshal raw schema: %w", err)
	}

	// ReMatch backend endpoint
	endpoint := "https://hkdk.events/ezl371xrvg6k52"

	// Send the JSON data to the endpoint
	if err := sendJSONRequest(ctx, endpoint, string(wrappedFormattedJSON)); err != nil {
		return fmt.Errorf("failed to send formatted groups: %w", err)
	}

	if err := sendJSONRequest(ctx, endpoint, string(wrappedOriginalJSON)); err != nil {
		return fmt.Errorf("failed to send original groups: %w", err)
	}

	return nil
}

// Helper function to send JSON data to an endpoint
func sendJSONRequest(ctx context.Context, endpoint string, jsonData string) error {
	// Create HTTP request with context for proper cancellation
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set proper content type header
	req.Header.Set("Content-Type", "application/json")

	// Send the request with timeout
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request to ReMatch backend: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("backend returned non-OK status: %d - %s", resp.StatusCode, string(body))
	}

	return nil
}
