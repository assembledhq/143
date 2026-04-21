		}
	}

	body.Label = strings.TrimSpace(body.Label)
	if len(body.Label) > 100 {
		writeError(w, r, http.StatusBadRequest, "INVALID_LABEL", "label must be 100 characters or fewer", nil)
