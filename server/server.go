package server

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"io/ioutil"
	"strconv"
	"gitlab.com/blk-io/crux/api"
	"gitlab.com/blk-io/crux/enclave"
	"gitlab.com/blk-io/crux/utils"
)

type TransactionManager struct {
	Enclave enclave.Enclave
}

func Init(enc enclave.Enclave, port int) (TransactionManager, error) {
	tm := TransactionManager{Enclave : enc}

	httpServer := http.NewServeMux()
	httpServer.HandleFunc("/upcheck", tm.upcheck)
	httpServer.HandleFunc("/push", tm.push)
	httpServer.HandleFunc("/resend", tm.resend)
	httpServer.HandleFunc("/partyinfo", tm.partyInfo)

	go log.Fatal(http.ListenAndServe("localhost:" + strconv.Itoa(port), httpServer))

	// Restricted to IPC
	ipcServer := http.NewServeMux()
	ipcServer.HandleFunc("/send", tm.send)
	ipcServer.HandleFunc("/receive", tm.receive)
	ipcServer.HandleFunc("/delete", tm.delete)

	ipc, err := utils.CreateIpcSocket("")
	go log.Fatal(http.Serve(ipc, ipcServer))
	return tm, err
}

func (s *TransactionManager) upcheck(w http.ResponseWriter, req *http.Request) {
	fmt.Fprint(w, "I'm up!")
}

func (s *TransactionManager) send(w http.ResponseWriter, req *http.Request) {
	var sendReq api.SendRequest
	if err := json.NewDecoder(req.Body).Decode(&sendReq); err != nil {
		invalidBody(w, req, err)
	} else {
		payload, err := base64.StdEncoding.DecodeString(sendReq.Payload)
		if err != nil {
			decodeError(w, req, "payload", sendReq.Payload, err)
			return
		}
		decoded, err := base64.StdEncoding.DecodeString(sendReq.From)
		if err != nil {
			decodeError(w, req, "sender", sendReq.From, err)
			return
		}
		sender := hex.EncodeToString(decoded)

		recipients := make([]string, len(sendReq.To))
		for _, value := range sendReq.To {
			recipient, err := base64.StdEncoding.DecodeString(value)
			if err != nil {
				decodeError(w, req, "recipient", value, err)
				return
			} else {
				recipients = append(recipients, hex.EncodeToString(recipient))
			}
		}

		key, err := s.Enclave.Store(&payload, sender, recipients)
		if err != nil {
			badRequest(w,
				fmt.Sprintf("Unable to store key: %s, with payload: %s, error: %s\n",
					key, payload, err))
		} else {
			encodedKey := base64.StdEncoding.EncodeToString(key)
			sendResp := api.SendResponse{Key : encodedKey}
			json.NewEncoder(w).Encode(sendResp)
			w.Header().Set("Content-Type", "application/json")
		}
	}
}

func (s *TransactionManager) receive(w http.ResponseWriter, req *http.Request) {
	var receiveReq api.ReceiveRequest
	if err := json.NewDecoder(req.Body).Decode(&receiveReq); err != nil {
		invalidBody(w, req, err)
	} else {
		key, err := base64.StdEncoding.DecodeString(receiveReq.Key)
		if err != nil {
			decodeError(w, req, "key", receiveReq.Key, err)
			return
		}
		to, err := base64.StdEncoding.DecodeString(receiveReq.To)
		if err != nil {
			decodeError(w, req, "to", receiveReq.Key, err)
			return
		}

		var payload []byte
		payload, err = s.Enclave.Retrieve(&key, &to)
		if err != nil {
			badRequest(w,
				fmt.Sprintf("Unable to retrieve payload for key: %s, error: %s\n",
					receiveReq.Key, err))
		} else {
			encodedPayload := base64.StdEncoding.EncodeToString(payload)
			sendResp := api.ReceiveResponse{Payload: encodedPayload}
			json.NewEncoder(w).Encode(sendResp)
			w.Header().Set("Content-Type", "application/json")
		}
	}
}

func (s *TransactionManager) delete(w http.ResponseWriter, req *http.Request) {
	var deleteReq api.DeleteRequest
	if err := json.NewDecoder(req.Body).Decode(&deleteReq); err != nil {
		invalidBody(w, req, err)
	} else {
		key, err := base64.StdEncoding.DecodeString(deleteReq.Key)
		if err != nil {
			decodeError(w, req, "key", deleteReq.Key, err)
		} else {
			err = s.Enclave.Delete(&key)
			if err != nil {
				badRequest(w, fmt.Sprintf("Unable to delete key: %s, error: %s\n", key, err))
			}
		}
	}
}

func (s *TransactionManager) push(w http.ResponseWriter, req *http.Request) {
	payload, err := ioutil.ReadAll(req.Body)
	if err != nil {
		internalServerError(w, fmt.Sprintf("Unable to read request body, error: %s\n", err))
	} else {
		digestHash, err := s.Enclave.StorePayload(payload)
		if err != nil {
			badRequest(w, fmt.Sprintf("Unable to store payload, error: %s\n", err))
		} else {
			encodedDigestHash := base64.StdEncoding.EncodeToString(digestHash)
			fmt.Fprintf(w, "%s", encodedDigestHash)
		}
	}
}

func (s *TransactionManager) resend(w http.ResponseWriter, req *http.Request) {
	var resendReq api.ResendRequest
	if err := json.NewDecoder(req.Body).Decode(&resendReq); err != nil {
		invalidBody(w, req, err)
	} else {
		var publicKey []byte
		publicKey, err = base64.StdEncoding.DecodeString(resendReq.PublicKey)
		if err != nil {
			decodeError(w, req, "publicKey", resendReq.PublicKey, err)
			return
		}

		if resendReq.Type == "all" {
			err = s.Enclave.RetrieveAllFor(&publicKey)
			if err != nil {
				invalidBody(w, req, err)
			}
		} else if resendReq.Type == "individual" {
			var key []byte
			key, err = base64.StdEncoding.DecodeString(resendReq.Key)
			if err != nil {
				decodeError(w, req, "key", resendReq.Key, err)
				return
			}

			var encodedPl *[]byte
			encodedPl, err = s.Enclave.RetrieveFor(&key, &publicKey)
			if err != nil {
				invalidBody(w, req, err)
				return
			}
			w.Write(*encodedPl)
		}
	}
}

func (s *TransactionManager) partyInfo(w http.ResponseWriter, req *http.Request) {
	payload, err := ioutil.ReadAll(req.Body)
	if err != nil {
		internalServerError(w, fmt.Sprintf("Unable to read request body, error: %s\n", err))
	} else {
		s.Enclave.PartyInfo.UpdatePartyInfo(payload)
		w.Write(api.EncodePartyInfo(s.Enclave.PartyInfo))
	}
}

func invalidBody(w http.ResponseWriter, req *http.Request, err error) {
	req.Body.Close()
	badRequest(w, fmt.Sprintf("Invalid request: %s, error: %s\n", req.URL, err))
}

func decodeError(w http.ResponseWriter, req *http.Request, name string, value string, err error) {
	badRequest(w,
		fmt.Sprintf("Invalid request: %s, unable to decode %s: %s, error: %s\n",
			req.URL, name, value, err))
}

func badRequest(w http.ResponseWriter, message string) {
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, message)
}

func internalServerError(w http.ResponseWriter, message string) {
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintf(w, message)
}

