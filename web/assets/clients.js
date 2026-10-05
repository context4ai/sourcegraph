// Connection examples use the current deployment. Browser cookies are never exported.
(function () {
  'use strict';
  var endpoint=location.origin+'/sourcegraph', url=endpoint+'/mcp?plugins=auto', REPO=window.DEPLOYMENT.example_repo;
  // null follows the access policy: a token is required when anonymous reads are off.
  var tokenChoice=null;
  function withToken(){return tokenChoice!==null?tokenChoice:(!window.API || !API.policy.public_read);}
  function configurations() {
    var secured=withToken(), token='Authorization: Bearer <service-token>';
    var header=function(pad){return secured?',\n'+pad+'"headers": { "Authorization": "Bearer <service-token>" }':'';};
    return [
      {id:'mcp',label:'mcp.json',code:'{\n  "mcpServers": {\n    "context-sourcegraph": {\n      "type": "http",\n      "url": "'+url+'"'+header('      ')+'\n    }\n  }\n}',note:['StreamableHTTP 类型 MCP 的配置方法，放进客户端的 MCP 配置文件或平台配置即可启用','StreamableHTTP MCP configuration. Put it in your client\'s MCP config file or platform settings to enable it']},
      {id:'claude',label:'Claude Code',code:'claude mcp add --transport http context-sourcegraph \"'+url+'\"'+(secured?" \\\n  --header '"+token+"'":'')},
      {id:'codex',label:'Codex',code:'# ~/.codex/config.toml\n[mcp_servers.context-sourcegraph]\nurl = "'+url+'"'+(secured?'\nbearer_token_env_var = "SOURCEGRAPH_API_TOKEN"':'')},
      {id:'cursor',label:'Cursor',code:'// ~/.cursor/mcp.json\n{\n  "mcpServers": {\n    "context-sourcegraph": {\n      "url": "'+url+'"'+header('      ')+'\n    }\n  }\n}'},
      {id:'bash',label:'Bash',code:('export SOURCEGRAPH_URL="'+endpoint+'"\n')+(secured?'export SOURCEGRAPH_API_TOKEN="<service-token>"\n':'')+'\n'+'# 参数与 rg 相同：-F -i -l -g，PATTERN 之后可以跟目录\nsourcegraph-cli search --repo '+REPO+' \\\n  -g \'*.ts\' \'OnboardingVersion\' src\n\n# 用搜索结果里的 Meta.Commit 继续读\nsourcegraph-cli read --repo '+REPO+' --revision <commit> \\\n  --path src/services/onboarding.ts --start-line 1 --end-line 40'},
      {id:'http',label:'HTTP',code:"curl -s -X POST '"+endpoint+"/api/search?repo="+encodeURIComponent(REPO)+"'"+(secured?" \\\n  -H '"+token+"'":'')+" \\\n  -H 'Content-Type: application/json' \\\n  -d '{\"Pattern\":\"timeout\",\"IgnoreCase\":true,\"Paths\":[\"src\"]}'"}
    ];
  }
  window.Clients={URL:url,get withToken(){return withToken();},set withToken(v){tokenChoice=v===null?null:!!v;},get list(){return configurations();},byId:function(id){var list=configurations();return list.find(function(c){return c.id===id;})||list[0];}};
})();
