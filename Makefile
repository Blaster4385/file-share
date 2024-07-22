.PHONY: fileshare clean

fileshare:
	rm -rf server/dist
	cp -r client server/dist
	cd server && go build -ldflags "-s -w"
	mv server/fileshare fileshare

clean:
	rm -rf server/dist
	rm fileshare
