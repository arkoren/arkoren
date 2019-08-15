import { AppServiceProvider } from '../app/Http/Providers/AppServiceProvider.ts';
import { RouteServiceProvider } from '../app/Http/Providers/RouteServiceProvider.ts';
export default {
    host: '127.0.0.1',
    port: 9000,
    name: 'My Application',
    env: 'development',
    debug: true,
    url: 'http://localhost',
    asset_url: null,
    providers: [
        AppServiceProvider,
        RouteServiceProvider
    ]
};
//# sourceMappingURL=app.js.map