package skill

// BuiltinSkills returns the skills that ship with edoc.
// They are registered with lower priority than user-defined skills
// (Registry.Register won't overwrite existing names).
func BuiltinSkills() []*Skill {
	return []*Skill{
		commitSkill(),
		reviewSkill(),
	}
}

func commitSkill() *Skill {
	return &Skill{
		Name:        "commit",
		Description: "Stage all changes and create a git commit with an AI-generated message",
		WhenToUse:   "When the user asks to commit, save changes to git, or says things like '提交代码', 'commit this', 'save my work to git'",
		Content: `Create a git commit for the current changes. Follow these steps:

1. Run "git status" and "git diff --staged" and "git diff" to understand all changes (staged and unstaged).
2. Run "git log --oneline -5" to see recent commit message style.
3. Analyze the changes and draft a concise commit message that:
   - Summarizes the nature of the changes (new feature, bug fix, refactor, etc.)
   - Focuses on the "why" rather than the "what"
   - Follows the repository's existing commit message style
   - Is 1-2 sentences
4. Stage the relevant files with "git add" (prefer specific files over "git add -A" to avoid committing secrets or large binaries).
   - Do NOT stage files that look like secrets (.env, credentials.json, etc.)
5. Create the commit.
6. Show the result with "git log --oneline -1".

If $ARGUMENTS is provided, use it as the commit message instead of generating one.

Important:
- NEVER use --no-verify or --no-gpg-sign
- NEVER amend existing commits unless explicitly asked
- If there are no changes to commit, inform the user`,
	}
}

func reviewSkill() *Skill {
	return &Skill{
		Name:        "review",
		Description: "Review code changes (git diff) for bugs, issues, and improvements",
		WhenToUse:   "When the user asks to review code, review changes, review a PR, check for bugs, or says things like '帮我review', 'review this', 'check my code'",
		Content: `Review the current code changes for bugs, issues, and improvements.

1. Run "git diff $ARGUMENTS" to get the changes to review.
   - If $ARGUMENTS is empty, use "git diff" for unstaged changes.
   - If $ARGUMENTS is a branch name or commit ref, diff against that.
2. Analyze the diff carefully and provide feedback on:
   - **Bugs**: Logic errors, off-by-one errors, null/nil dereference, race conditions
   - **Security**: SQL injection, XSS, hardcoded secrets, unsafe operations
   - **Performance**: N+1 queries, unnecessary allocations, missing indexes
   - **Style**: Naming conventions, code organization, missing error handling
   - **Correctness**: Edge cases, missing validation, incorrect assumptions
3. For each issue found, reference the specific file and line, explain the problem, and suggest a fix.
4. If the code looks good, say so — don't invent problems.

Be concise and actionable. Prioritize real bugs over style nitpicks.`,
	}
}
