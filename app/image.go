package app

// func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request) {
// 	bs, err := ioutil.ReadAll(r.Body)

// 	if err != nil {
// 		log.Error().Err(err).Msgf("uploadImage:ioutil.ReadAll [%v]", err.Error())
// 		render.Render(w, r, ErrInvalidRequest(err))
// 		return
// 	}

// 	url, err := s.Bucket.UploadImage(string(bs))
// 	if err != nil {
// 		log.Error().Err(err).Msgf("uploadImage:s.Bucket.UploadImage [%v]", err.Error())
// 		render.Render(w, r, ErrInvalidRequest(err))
// 		return
// 	}

// 	if err := render.Render(w, r, NewImageResponse(http.StatusText(http.StatusCreated), url)); err != nil {
// 		render.Render(w, r, ErrRender(err))
// 		return
// 	}

// }
