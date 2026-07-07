---
name: grill
description: Grill the user relentlessly about a plan or design, one question at a time, until you reach a shared understanding. Looks up facts in the codebase itself and only puts decisions to the user. Use when the user wants to stress-test a plan before building, or uses any 'grill' trigger phrase (grill me, grill this, stress-test my plan).
---

# /grill — stress-test a plan before building it

Interview the user relentlessly about every aspect of this plan until you reach
a shared understanding. Walk down each branch of the design tree, resolving
dependencies between decisions one-by-one. For each question, provide your
recommended answer. Maximum 10 questions, unless the user has said they want to
answer more.

Ask the questions **one at a time**, waiting for feedback on each before
continuing. Asking multiple questions at once is bewildering.

If a *fact* can be found by exploring the codebase, look it up rather than
asking. The *decisions*, though, are the user's — put each one to them and wait
for their answer.

Do not enact the plan until the user confirms you have reached a shared
understanding.
