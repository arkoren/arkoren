import { Middleware, Request, Response } from '../../../../framework/http.ts'

/**
 * Implements the Log middleware.
 *
 * @export
 * @class Log
 * @implements {Middleware}
 */
export default class Log extends Middleware {

    /**
     * Runs before the route handler.
     *
     * @param {Request} request
     * @memberof Log
     */
    before(request: Request) {
        console.log('Log before...')
    }

    /**
     * Runs after the route handler.
     *
     * @param {Request} request
     * @param {Response} response
     * @memberof Log
     */
    after(request: Request, response: Response) {
        console.log('Log after...')
    }

}
