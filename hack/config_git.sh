GITHUB_REPO_URL=git@github.com:ctfer-io/ctfer.git
GITEA_REPO_URL=ssh://gitea@git.ctfer-io.lab:2222/ctfer-io/ctfer.git

echo "[+] Create backup remote"
git remote rename origin backup

echo "[+] Create new origin remote"
git remote add origin $GITHUB_REPO_URL
git remote set-url --add --push origin $GITHUB_REPO_URL
git remote set-url --add --push origin $GITEA_REPO_URL

git remote show origin

echo "[+] $0 just finish, make sure that you use origin remote"

# Git fetch ?

exit 0