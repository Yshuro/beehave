## What the queue audit must do

You audit the open work of the project before pass {{ .pass }} of the round on the base `{{ .base }}`. You change no file, no issue and no pull request.

Read the open issues and the open pull requests with the forge tool. Report each item that the queue would give out wrongly: an issue that a merged pull request already fixed, a pull request with no issue, and an item with a label that blocks it.

Say in your report what you found. Write "nothing found" when the open work is as the queue shows it.
