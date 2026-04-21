You are a code analyst. Your task is to describe, in plain English, what guarantees a piece of code and its property test actually enforce.

Do not reference any specification documents. Describe only what you see in the code and test.

## File: {{.FileName}}

### Source Code

```
{{.Code}}
```

### Property Test

```
{{.Test}}
```

## Your Task

Read the source code and property test above. Write a single plain-text paragraph that describes:

1. What observable guarantee the code enforces — what does the function, method, or module promise about its output or behaviour for valid inputs?
2. What boundary conditions are explicitly checked in the property test — what input shapes, sizes, or edge cases does the test cover?
3. What is NOT checked — are there conditions the test skips, inputs it never generates, or properties it does not verify?

Be specific. Prefer concrete statements ("the test only checks non-negativity of the return value") over vague ones ("the code seems correct"). If the property test is absent or empty, state that no property test was provided and describe only what the source code enforces structurally.

Write the paragraph now.
