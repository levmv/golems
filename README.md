# Golems

Monorepo for some personal chatbots/ai-agents related work.

**Abot**  
Simplest stateless telegram-llm-chatbot.

**Ann**  
Chat-bot with evolving personality. Multiple bots. History. Context-compaction. Decoupled messaging channels.

**Brevity**  
Summarization for web pages

**Cy**  
Draft CLI ai agent

**Chore**  
?

**Caliban**  
Personal assistant

**Hugin** 
Monitoring

**UI** 
Chat web UI, split out into its own repository: [levmv/murm-ui](https://github.com/levmv/murm-ui)


## Packages
* llm      - Core abstractions over AI providers
* golem    - Reusable conversational agent loop
* jsonschema - JSON Schema helpers for tools and structured outputs
* logger   - Just simple text logger
* openai   - OpenAI API package
* telegram - Telegram Bot API
* tasks    - Durable task queue with scheduling
